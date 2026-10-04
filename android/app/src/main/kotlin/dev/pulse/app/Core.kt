package dev.pulse.app

import android.Manifest
import android.app.Application
import android.bluetooth.BluetoothAdapter
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.util.Log
import org.json.JSONArray
import org.json.JSONObject

class PulseApp : Application() {
    override fun onCreate() {
        super.onCreate()
        Core.init(this)
    }
}

/**
 * Everything that outlives the activity: the Bluetooth scanner and advertiser,
 * the page's session, and the native motion stream that takes over while the
 * page can't run (screen off). All of it is driven from the main thread.
 */
object Core {
    const val TAG = "PulseApp"
    const val DEFAULT_URL = "http://localhost:8080/"

    lateinit var app: Context
        private set
    lateinit var prefs: SharedPreferences
        private set
    lateinit var ble: Ble
        private set
    val main = Handler(Looper.getMainLooper())

    /** The page's own hello message (session id + position), kept in memory only. Null = not joined. */
    @Volatile
    var hello: String? = null
        private set

    @Volatile
    var pageVisible = true
        private set

    /**
     * What happens when the page is no longer visible:
     * native = pause the page, stream motion from native code;
     * page = leave the page alone (to measure what a WebView does by itself);
     * keepvisible = tell the WebView its window is still visible (measurement only).
     */
    @Volatile
    var screenOffMode = "native"

    /** Delivers one event (a JSON object) to the page. Set by the activity. */
    @Volatile
    var toPage: ((String) -> Unit)? = null

    private var link: NativeLink? = null
    private var motion: NativeMotion? = null
    private var wake: PowerManager.WakeLock? = null

    // What native code did while the page was away (shown by the page afterwards).
    @Volatile
    private var offStretches = 0
    @Volatile
    private var offSentBefore = 0L
    @Volatile
    private var offMs = 0L
    private var offSince = 0L
    @Volatile
    private var lastZone = ""
    private var inDanger = false

    var serverUrl: String
        get() = prefs.getString("url", null) ?: DEFAULT_URL
        set(v) {
            prefs.edit().putString("url", v).apply()
        }

    fun init(ctx: Context) {
        app = ctx.applicationContext
        prefs = app.getSharedPreferences("pulse", Context.MODE_PRIVATE)
        ble = Ble(app) { emitStatus() }
        val r = object : BroadcastReceiver() {
            override fun onReceive(c: Context?, i: Intent?) {
                ble.onAdapterChanged()
            }
        }
        val f = IntentFilter(BluetoothAdapter.ACTION_STATE_CHANGED)
        if (Build.VERSION.SDK_INT >= 33) app.registerReceiver(r, f, Context.RECEIVER_EXPORTED) else app.registerReceiver(r, f)
    }

    fun has(permission: String) = app.checkSelfPermission(permission) == PackageManager.PERMISSION_GRANTED

    /** ws(s)://host/ws/phone for the configured server. */
    fun wsUrl(): String {
        val u = Uri.parse(serverUrl)
        val scheme = if (u.scheme == "https") "wss" else "ws"
        return "$scheme://${u.encodedAuthority}/ws/phone"
    }

    // ---- called by the bridge (on the main thread) ----

    fun setSession(helloJson: String) {
        val ok = helloJson.length in 2..2000 && runCatching { JSONObject(helloJson).optString("id").isNotEmpty() }.getOrDefault(false)
        val was = hello
        hello = if (ok) helloJson else null
        if (ok && was == null) Log.i(TAG, "joined: session " + JSONObject(helloJson).optString("id"))
        if (!ok) stopNative()
        syncService()
    }

    fun setScan(on: Boolean) {
        ble.setScan(on)
        main.removeCallbacks(tick)
        if (on) main.postDelayed(tick, 1000)
        syncService()
    }

    fun setAdvertise(id: String) {
        ble.setAdvertise(id)
        syncService()
    }

    /** Leave: stop everything and drop the session. */
    fun stopAll() {
        hello = null
        stopNative()
        ble.setScan(false)
        ble.setAdvertise("")
        main.removeCallbacks(tick)
        releaseWake()
        syncService()
        emitStatus()
    }

    private fun wanted() = hello != null || ble.scanWanted || ble.advertWanted != null

    /** The foreground service runs exactly while there is something to keep alive. */
    fun syncService() {
        try {
            if (wanted()) {
                if (PulseService.instance == null) app.startForegroundService(Intent(app, PulseService::class.java))
                else PulseService.instance?.refresh()
            } else {
                app.stopService(Intent(app, PulseService::class.java))
            }
        } catch (e: Exception) {
            Log.w(TAG, "service: $e")
        }
    }

    // ---- once a second while scanning: the smoothed beacon report ----

    private val tick = object : Runnable {
        override fun run() {
            if (!ble.scanWanted) return
            main.postDelayed(this, 1000)
            if (!ble.scanning) return
            val seen = JSONArray()
            for (t in ble.read()) seen.put(JSONObject().put("name", t.name).put("rssi", t.rssi).put("n", t.n))
            val msg = JSONObject().put("type", "beacons").put("seen", seen).toString()
            // Same message either way: the page puts it on its WebSocket, or (screen off) the native link does.
            val l = link
            if (l != null) l.send(msg) else toPage?.invoke(msg)
        }
    }

    // ---- page visible / not visible ----

    fun pageHidden() {
        if (!pageVisible) return
        pageVisible = false
        if (!wanted()) return
        acquireWake()
        val h = hello
        if (h != null && screenOffMode == "native") {
            offStretches++
            offSince = System.currentTimeMillis()
            val l = NativeLink(wsUrl(), h) { onState(it) }
            val m = NativeMotion(app, l)
            link = l
            motion = m
            l.start()
            m.start()
            Log.i(TAG, "screen off: native motion stream started (${wsUrl()})")
        } else {
            Log.i(TAG, "page hidden, mode=$screenOffMode, joined=${h != null}: page left to itself")
        }
        PulseService.instance?.refresh()
    }

    fun pageShown() {
        if (pageVisible) return
        stopNative()
        releaseWake()
        pageVisible = true
        PulseService.instance?.refresh()
        emitStatus()
    }

    private fun stopNative() {
        val l = link ?: return
        motion?.stop()
        motion = null
        l.close()
        link = null
        offSentBefore += l.sent.get()
        if (offSince > 0) offMs += System.currentTimeMillis() - offSince
        offSince = 0
        setDanger(false, "")
        Log.i(TAG, "screen on: native stream stopped after ${l.sent.get()} readings, ${l.connects.get()} connection(s); back to the page")
    }

    private fun acquireWake() {
        if (wake?.isHeld == true) return
        val pm = app.getSystemService(Context.POWER_SERVICE) as PowerManager
        wake = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "pulse:running").apply {
            setReferenceCounted(false)
            acquire(6 * 60 * 60 * 1000L) // an event, not forever
        }
    }

    private fun releaseWake() {
        runCatching { if (wake?.isHeld == true) wake?.release() }
        wake = null
    }

    /** A state message from the server on the native link (any thread). */
    private fun onState(s: JSONObject) {
        main.post {
            val zone = s.optString("zone")
            lastZone = zone
            val move = s.optJSONObject("move")
            val danger = zone == "red" || move != null
            val to = move?.optString("to").orEmpty()
            setDanger(danger, if (to.isNotEmpty()) "Move toward $to. Tap to see the arrow." else "Move sideways, out of the push. Tap to see the arrow.")
        }
    }

    private fun setDanger(on: Boolean, text: String) {
        if (on == inDanger) return
        inDanger = on
        PulseService.instance?.danger(on, text)
    }

    // ---- status ----

    fun status(): JSONObject {
        val sdk = Build.VERSION.SDK_INT
        val l = link
        val o = JSONObject()
        o.put("app", true)
        o.put("version", BuildConfig.VERSION_NAME)
        o.put("sdk", sdk)
        o.put("bluetooth", JSONObject()
            .put("supported", ble.supported())
            .put("on", ble.enabled())
            .put("canScan", ble.canScan())
            .put("canAdvertise", ble.canAdvertise()))
        o.put("permissions", JSONObject()
            .put("bluetooth", ble.canScan() && ble.canAdvertise())
            .put("location", has(Manifest.permission.ACCESS_FINE_LOCATION))
            .put("notifications", sdk < 33 || has(Manifest.permission.POST_NOTIFICATIONS)))
        o.put("scanning", ble.scanning)
        o.put("scanError", ble.scanError)
        o.put("adverts", ble.adverts)
        o.put("advertising", ble.advertising)
        o.put("advertId", ble.advertId)
        o.put("advertError", ble.advertError)
        o.put("service", PulseService.instance != null)
        o.put("joined", hello != null)
        o.put("screenOffMode", screenOffMode)
        o.put("native", JSONObject()
            .put("active", l != null)
            .put("connected", l?.open == true)
            .put("sent", l?.sent?.get() ?: 0))
        // The last stretches with the page away: how often, for how long, how many readings went out natively.
        o.put("screenOff", JSONObject()
            .put("times", offStretches)
            .put("seconds", Math.round((offMs + if (offSince > 0) System.currentTimeMillis() - offSince else 0) / 1000.0))
            .put("sent", offSentBefore + (l?.sent?.get() ?: 0)))
        return o
    }

    fun emitStatus() {
        val f = toPage ?: return
        val s = status().put("type", "status").toString()
        main.post { f(s) }
        PulseService.instance?.refresh()
    }

    /** One line for the notification. */
    fun summary(): String {
        val parts = ArrayList<String>()
        if (link != null) parts += if (link?.open == true) "streaming with the screen off" else "screen off, reconnecting"
        if (ble.scanning) parts += "hearing venue beacons"
        if (ble.advertising) parts += "visible to venue beacons"
        return if (parts.isEmpty()) "Tap to open" else parts.joinToString(" · ").replaceFirstChar { it.uppercase() }
    }
}
