package dev.pulse.app

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.graphics.Color
import android.graphics.Typeface
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.text.InputType
import android.util.Log
import android.util.TypedValue
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.webkit.ConsoleMessage
import android.webkit.GeolocationPermissions
import android.webkit.JavascriptInterface
import android.webkit.PermissionRequest
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** The WebView. In the "keepvisible" measuring mode it is told its window never went away. */
@SuppressLint("ViewConstructor")
class PulseWebView(ctx: Context) : WebView(ctx) {
    var keepVisible = false
    override fun onWindowVisibilityChanged(visibility: Int) {
        super.onWindowVisibilityChanged(if (keepVisible) View.VISIBLE else visibility)
    }
}

/**
 * One activity: a first-launch screen (server address, what will be asked for
 * and why), then the Pulse phone page in a WebView with window.PulseNative.
 */
class MainActivity : Activity() {
    companion object {
        private const val REQ_SETUP = 1
        private const val REQ_GEO = 2
        private const val REQ_BRIDGE = 3
        const val ACTION_SETUP = "dev.pulse.app.SETUP"
    }

    private var web: PulseWebView? = null
    private var geoPending: Pair<String, GeolocationPermissions.Callback>? = null
    private var showingSetup = false
    private var started = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // The wake-lock equivalent: the screen stays on while Pulse is in front.
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        // A crowd warning must be readable without unlocking the phone.
        if (Build.VERSION.SDK_INT >= 27) setShowWhenLocked(true)
        else @Suppress("DEPRECATION") window.addFlags(WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED)
        if ((applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE) != 0) WebView.setWebContentsDebuggingEnabled(true)
        route(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        if (intent.action == ACTION_SETUP || intent.hasExtra("url") || intent.hasExtra("screenOff")) route(intent)
    }

    /** Dev aids: `am start … --es url https://… --es screenOff native|page|keepvisible`. */
    private fun route(intent: Intent) {
        intent.getStringExtra("screenOff")?.let {
            if (it in listOf("native", "page", "keepvisible")) Core.screenOffMode = it
            Log.i(Core.TAG, "screen-off mode: ${Core.screenOffMode}")
        }
        val url = intent.getStringExtra("url")?.let { cleanUrl(it) }
        if (url != null) {
            Core.serverUrl = url
            Core.prefs.edit().putBoolean("setupDone", true).apply()
        }
        if (intent.action == ACTION_SETUP || !Core.prefs.getBoolean("setupDone", false)) showSetup(null)
        else showWeb(reload = url != null)
    }

    private fun cleanUrl(s: String): String? {
        var t = s.trim()
        if (t.isEmpty()) return null
        if (!t.contains("://")) t = (if (t.startsWith("localhost") || t.startsWith("127.0.0.1")) "http://" else "https://") + t
        val u = Uri.parse(t)
        if ((u.scheme != "http" && u.scheme != "https") || u.host.isNullOrEmpty()) return null
        return t
    }

    // ---- first launch (and "Change server") ----

    private fun dp(v: Int) = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v.toFloat(), resources.displayMetrics).toInt()

    private fun text(s: String, size: Float, color: Int = Color.WHITE, bold: Boolean = false) = TextView(this).apply {
        text = s
        setTextSize(TypedValue.COMPLEX_UNIT_SP, size)
        setTextColor(color)
        if (bold) typeface = Typeface.DEFAULT_BOLD
        setPadding(0, dp(6), 0, dp(6))
        setLineSpacing(0f, 1.15f)
    }

    private fun showSetup(problem: String?) {
        showingSetup = true
        val grey = Color.parseColor("#9aa7b4")
        val col = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(24), dp(40), dp(24), dp(24))
        }
        col.addView(text("Pulse", 34f, bold = true))
        col.addView(text("Early warning for crowd crushes. This app is the Pulse page plus two things a web page can't do: Bluetooth, and running with the screen off.", 16f, grey))
        if (problem != null) col.addView(text(problem, 15f, Color.parseColor("#f87171")))

        col.addView(text("Event server", 14f, grey).apply { setPadding(0, dp(18), 0, 0) })
        val url = EditText(this).apply {
            setText(Core.serverUrl)
            setSingleLine()
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            setTextColor(Color.WHITE)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 17f)
        }
        col.addView(url)

        val missing = missingSetupPermissions()
        if (missing.isNotEmpty()) {
            col.addView(text("Next, Android will ask you:", 16f, bold = true).apply { setPadding(0, dp(18), 0, 0) })
            if (missing.any { it.startsWith("android.permission.BLUETOOTH") })
                col.addView(text("• Nearby devices (Bluetooth). Lets Pulse hear the venue's beacons, and lets them hear this phone, to place you in the room. Tap Allow.", 15f, grey))
            if (Build.VERSION.SDK_INT < 31 && missing.contains(Manifest.permission.ACCESS_FINE_LOCATION))
                col.addView(text("• Location. On this Android version, Bluetooth scanning needs it. Tap While using the app.", 15f, grey))
            if (missing.contains(Manifest.permission.POST_NOTIFICATIONS))
                col.addView(text("• Notifications. Pulse shows one the whole time it is running, and warns you with the screen off. Tap Allow.", 15f, grey))
        }
        col.addView(text("Location is only asked for later, and only if this venue uses GPS. No name, contacts, audio or photos. You can say no to anything: Pulse still works like the web page.", 14f, grey))

        val go = Button(this).apply {
            text = "Continue"
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 18f)
            setOnClickListener {
                val u = cleanUrl(url.text.toString())
                if (u == null) {
                    url.error = "Enter the address of the Pulse server, e.g. https://pulse.example"
                    return@setOnClickListener
                }
                Core.serverUrl = u
                Core.prefs.edit().putBoolean("setupDone", true).apply()
                val ask = missingSetupPermissions()
                if (ask.isEmpty()) showWeb(reload = true) else requestPermissions(ask.toTypedArray(), REQ_SETUP)
            }
        }
        col.addView(go, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(56)).apply { topMargin = dp(18) })
        setContentView(ScrollView(this).apply { addView(col) })
    }

    /** What the first-launch screen asks for: Bluetooth, and notifications on Android 13+. Never location on Android 12+. */
    private fun missingSetupPermissions(): List<String> {
        val want = ArrayList<String>()
        if (Build.VERSION.SDK_INT >= 31) {
            want += Manifest.permission.BLUETOOTH_SCAN
            want += Manifest.permission.BLUETOOTH_ADVERTISE
            want += Manifest.permission.BLUETOOTH_CONNECT
        } else {
            want += Manifest.permission.ACCESS_FINE_LOCATION
        }
        if (Build.VERSION.SDK_INT >= 33) want += Manifest.permission.POST_NOTIFICATIONS
        return want.filter { !Core.has(it) }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        Log.i(Core.TAG, "permissions: " + permissions.indices.joinToString { permissions[it].substringAfterLast('.') + "=" + (grantResults.getOrNull(it) == 0) })
        when (requestCode) {
            REQ_SETUP -> showWeb(reload = true)
            REQ_GEO -> {
                val p = geoPending
                geoPending = null
                p?.second?.invoke(p.first, Core.has(Manifest.permission.ACCESS_FINE_LOCATION) || Core.has(Manifest.permission.ACCESS_COARSE_LOCATION), false)
            }
        }
        Core.ble.retry()
        Core.syncService()
        Core.emitStatus()
    }

    // ---- the page ----

    @SuppressLint("SetJavaScriptEnabled")
    private fun showWeb(reload: Boolean) {
        showingSetup = false
        var w = web
        if (w == null) {
            w = PulseWebView(this)
            web = w
            w.setBackgroundColor(Color.parseColor("#0b0f14"))
            w.settings.apply {
                javaScriptEnabled = true
                domStorageEnabled = true
                databaseEnabled = true
                setGeolocationEnabled(true)
                mediaPlaybackRequiresUserGesture = false
                mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
                userAgentString = "$userAgentString PulseApp/${BuildConfig.VERSION_NAME}"
            }
            w.addJavascriptInterface(Bridge(), "PulseNative")
            w.webViewClient = client
            w.webChromeClient = chrome
            Core.toPage = { json -> web?.evaluateJavascript("window.dispatchEvent(new CustomEvent('pulsenative',{detail:$json}))", null) }
            w.loadUrl(Core.serverUrl)
        } else if (reload) {
            Core.stopAll() // a new server is a new session
            w.loadUrl(Core.serverUrl)
        }
        w.keepVisible = Core.screenOffMode == "keepvisible"
        (w.parent as? ViewGroup)?.removeView(w)
        setContentView(w)
    }

    private val client = object : WebViewClient() {
        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            // Only the event server lives in here (it gets the Bluetooth bridge); anything else opens in the browser.
            if (request.url.host == Uri.parse(Core.serverUrl).host) return false
            runCatching { startActivity(Intent(Intent.ACTION_VIEW, request.url)) }
            return true
        }

        override fun onPageFinished(view: WebView, url: String) {
            Log.i(Core.TAG, "page loaded: $url")
        }

        override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
            if (!request.isForMainFrame) return
            Log.w(Core.TAG, "page failed: ${request.url} ${error.errorCode} ${error.description}")
            val why = if (error.description.toString().contains("CLEARTEXT")) "Plain http:// only works for localhost. Use the https:// address."
            else "Couldn't reach ${request.url.host} (${error.description})."
            showSetup(why)
        }
    }

    private val chrome = object : WebChromeClient() {
        override fun onGeolocationPermissionsShowPrompt(origin: String, callback: GeolocationPermissions.Callback) {
            // The page asked for GPS: only now is Android's location prompt shown.
            if (Core.has(Manifest.permission.ACCESS_FINE_LOCATION)) callback.invoke(origin, true, false)
            else {
                geoPending = origin to callback
                requestPermissions(arrayOf(Manifest.permission.ACCESS_FINE_LOCATION, Manifest.permission.ACCESS_COARSE_LOCATION), REQ_GEO)
            }
        }

        override fun onPermissionRequest(request: PermissionRequest) {
            request.deny() // camera/microphone: Pulse never uses them (WebRTC data channels need no permission)
        }

        override fun onConsoleMessage(m: ConsoleMessage): Boolean {
            Log.d(Core.TAG, "page console: ${m.message()} (${m.sourceId().substringAfterLast('/')}:${m.lineNumber()})")
            return true
        }
    }

    // ---- window.PulseNative ----

    /** Runs on the main thread and waits for it (bridge calls arrive on their own thread). */
    private fun <T> onMain(default: T, f: () -> T): T {
        var out = default
        val done = CountDownLatch(1)
        runOnUiThread {
            try { out = f() } catch (e: Exception) { Log.w(Core.TAG, "bridge: $e") }
            done.countDown()
        }
        done.await(2, TimeUnit.SECONDS)
        return out
    }

    inner class Bridge {
        /** JSON: permissions, Bluetooth on/off, what is running. */
        @JavascriptInterface
        fun status(): String = Core.status().toString()

        /** Start or stop listening for the venue's PULSE- boards. Results arrive as "pulsenative" events. Returns status(). */
        @JavascriptInterface
        fun scanBeacons(on: Boolean): String = onMain("{}") {
            Core.setScan(on)
            Core.status().toString()
        }

        /** Advertise this phone's session ("" stops). Returns status() (advertising turns true a moment later, with a status event). */
        @JavascriptInterface
        fun advertise(id: String): String = onMain("{}") {
            Core.setAdvertise(id)
            Core.status().toString()
        }

        /** The page's hello message, so native code can carry the session while the screen is off. "" = left. */
        @JavascriptInterface
        fun setSession(hello: String) {
            runOnUiThread { Core.setSession(hello) }
        }

        /** Show Android's Bluetooth prompt again (or the app's settings page once Android won't ask any more). */
        @JavascriptInterface
        fun requestPermissions() {
            runOnUiThread { askBluetooth() }
        }
    }

    private fun askBluetooth() {
        val ask = missingSetupPermissions()
        if (ask.isEmpty()) {
            Core.ble.retry()
            return
        }
        val asked = Core.prefs.getBoolean("askedOnce", false)
        if (asked && ask.none { shouldShowRequestPermissionRationale(it) }) {
            // Refused twice: Android no longer shows the prompt. Open this app's settings page instead.
            startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:$packageName")))
        } else {
            Core.prefs.edit().putBoolean("askedOnce", true).apply()
            requestPermissions(ask.toTypedArray(), REQ_BRIDGE)
        }
    }

    // ---- visible / not visible ----

    override fun onStart() {
        super.onStart()
        if (started) return
        started = true
        // Native streaming stops first; then the page wakes up and reconnects with the same session id.
        Core.pageShown()
        web?.let {
            it.resumeTimers()
            it.onResume()
        }
        Core.ble.retry()
    }

    override fun onStop() {
        super.onStop()
        started = false
        val w = web ?: return
        Core.pageHidden()
        if (Core.screenOffMode == "native") {
            // The page can't sample motion while hidden; pause it completely so it doesn't fight
            // the native connection for the session.
            w.onPause()
            w.pauseTimers()
        }
    }

    override fun onDestroy() {
        if (isFinishing) {
            Core.toPage = null
            Core.stopAll()
            web?.let {
                it.resumeTimers()
                it.destroy()
            }
            web = null
        }
        super.onDestroy()
    }

    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {
        val w = web
        if (!showingSetup && w != null && w.canGoBack()) w.goBack()
        else if (showingSetup && w != null && Core.prefs.getBoolean("setupDone", false)) showWeb(reload = false)
        else moveTaskToBack(true)
    }
}
