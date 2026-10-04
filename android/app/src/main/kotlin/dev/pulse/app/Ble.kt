package dev.pulse.app

import android.Manifest
import android.annotation.SuppressLint
import android.bluetooth.BluetoothManager
import android.bluetooth.le.AdvertiseCallback
import android.bluetooth.le.AdvertiseData
import android.bluetooth.le.AdvertiseSettings
import android.bluetooth.le.ScanCallback
import android.bluetooth.le.ScanFilter
import android.bluetooth.le.ScanResult
import android.bluetooth.le.ScanSettings
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log

/**
 * Bluetooth LE, both directions.
 *
 * Scan: hears the fixed Pulse boards (local name PULSE-…) and keeps a smoothed
 * signal strength per board, the same way web/phone/src/beacons.ts does
 * (median of the last 2 s, then an exponential moving average).
 *
 * Advertise: makes this phone hearable by the boards. Legacy, non-connectable,
 * no name; manufacturer data, company 0xFFFF, payload "PLS1" + the first 8 hex
 * characters of the Pulse session id (ASCII). 12 bytes of payload.
 *
 * Used from the main thread only (scan and advertise callbacks arrive there).
 */
@SuppressLint("MissingPermission") // every call is behind canScan()/canAdvertise() and a SecurityException catch
class Ble(private val ctx: Context, private val changed: () -> Unit) {
    companion object {
        const val PREFIX = "PULSE-"
        const val COMPANY = 0xFFFF
        const val WINDOW_MS = 2000L
        const val EMA = 0.35
        const val FORGET_MS = 5000L
        const val MAX_TRACKS = 16
        /** Android turns a scan that runs over 30 min into an opportunistic one: restart before that. */
        const val RESCAN_MS = 20 * 60 * 1000L
    }

    class Track(val name: String, val rssi: Double, val n: Int)

    private class T {
        val samples = ArrayList<LongArray>() // [time, rssi]
        var ema: Double? = null
        var at = 0L
    }

    private val handler = Handler(Looper.getMainLooper())
    private val tracks = HashMap<String, T>()
    private val names = HashMap<String, String>() // address → name, for adverts whose name sits in the scan response

    var scanWanted = false
        private set
    @Volatile
    var scanning = false
        private set
    @Volatile
    var scanError = ""
        private set
    @Volatile
    var adverts = 0L
        private set

    var advertWanted: String? = null
        private set
    @Volatile
    var advertising = false
        private set
    @Volatile
    var advertId = ""
        private set
    @Volatile
    var advertError = ""
        private set

    private fun adapter() = (ctx.getSystemService(Context.BLUETOOTH_SERVICE) as? BluetoothManager)?.adapter
    private fun has(p: String) = ctx.checkSelfPermission(p) == PackageManager.PERMISSION_GRANTED

    fun supported() = adapter() != null && ctx.packageManager.hasSystemFeature(PackageManager.FEATURE_BLUETOOTH_LE)
    fun enabled() = try { adapter()?.isEnabled == true } catch (e: SecurityException) { false }
    fun canScan() = if (Build.VERSION.SDK_INT >= 31) has(Manifest.permission.BLUETOOTH_SCAN) else has(Manifest.permission.ACCESS_FINE_LOCATION)
    fun canAdvertise() = Build.VERSION.SDK_INT < 31 || has(Manifest.permission.BLUETOOTH_ADVERTISE)

    // ---- scan ----

    fun setScan(on: Boolean) {
        scanWanted = on
        if (on) startScan() else stopScan()
        changed()
    }

    private val rescan = Runnable {
        if (scanning) {
            Log.i(Core.TAG, "scan: periodic restart")
            stopScan()
            startScan()
        }
    }

    private val scanCb = object : ScanCallback() {
        override fun onScanResult(callbackType: Int, result: ScanResult) {
            val addr = result.device?.address ?: return
            var name = result.scanRecord?.deviceName?.trim()
            if (name.isNullOrEmpty()) name = names[addr] else if (names.size < 64) names[addr] = name
            // The scan is filtered already; check again so nothing but a Pulse board can ever be reported.
            if (name == null || !name.startsWith(PREFIX) || name.length > 32) return
            val rssi = result.rssi
            if (rssi > -20 || rssi < -110) return
            val now = System.currentTimeMillis()
            var t = tracks[name]
            if (t == null) {
                if (tracks.size >= MAX_TRACKS) return
                t = T()
                tracks[name] = t
                Log.i(Core.TAG, "scan: first advert from $name, $rssi dBm")
            }
            t.samples.add(longArrayOf(now, rssi.toLong()))
            t.at = now
            adverts++
        }

        override fun onScanFailed(errorCode: Int) {
            scanning = false
            scanError = when (errorCode) {
                SCAN_FAILED_ALREADY_STARTED -> "already started"
                SCAN_FAILED_APPLICATION_REGISTRATION_FAILED -> "Bluetooth is busy (registration failed)"
                SCAN_FAILED_FEATURE_UNSUPPORTED -> "not supported on this phone"
                6 -> "scanning too often, try again in 30 s"
                else -> "error $errorCode"
            }
            Log.w(Core.TAG, "scan failed: $scanError")
            changed()
        }
    }

    private fun startScan() {
        if (scanning) return
        scanError = ""
        if (!supported()) { scanError = "this phone has no Bluetooth LE"; return }
        if (!canScan()) { scanError = "permission"; return }
        if (!enabled()) { scanError = "Bluetooth is off"; return }
        val scanner = adapter()?.bluetoothLeScanner
        if (scanner == null) { scanError = "Bluetooth is off"; return }
        // Filters matter: Android stops an unfiltered scan when the screen goes off.
        // A board is matched by its manufacturer marker (0xFFFF "PLS…") or by its name; names can't be
        // filtered by prefix, so the usual tags are listed, and the callback checks the prefix again.
        val filters = ArrayList<ScanFilter>()
        filters += ScanFilter.Builder().setManufacturerData(COMPANY, "PLS".toByteArray(), byteArrayOf(-1, -1, -1)).build()
        for (c in "ABCDEFGHS") filters += ScanFilter.Builder().setDeviceName("$PREFIX$c").build()
        val settings = ScanSettings.Builder().setScanMode(ScanSettings.SCAN_MODE_LOW_LATENCY).build()
        try {
            scanner.startScan(filters, settings, scanCb)
            scanning = true
            handler.removeCallbacks(rescan)
            handler.postDelayed(rescan, RESCAN_MS)
            Log.i(Core.TAG, "scan started (${filters.size} filters, low latency)")
        } catch (e: Exception) {
            scanError = e.message ?: e.toString()
            Log.w(Core.TAG, "scan start: $e")
        }
    }

    private fun stopScan() {
        handler.removeCallbacks(rescan)
        if (scanning) {
            try { adapter()?.bluetoothLeScanner?.stopScan(scanCb) } catch (e: Exception) { Log.w(Core.TAG, "scan stop: $e") }
            Log.i(Core.TAG, "scan stopped")
        }
        scanning = false
        if (!scanWanted) tracks.clear()
    }

    /** The boards heard in the last 5 s, strongest first. Call about once a second: each call advances the smoothing. */
    fun read(now: Long = System.currentTimeMillis()): List<Track> {
        val out = ArrayList<Track>()
        val iter = tracks.entries.iterator()
        while (iter.hasNext()) {
            val (name, t) = iter.next()
            if (now - t.at > FORGET_MS) { iter.remove(); continue }
            t.samples.removeAll { now - it[0] > WINDOW_MS }
            if (t.samples.isNotEmpty()) {
                val s = t.samples.map { it[1].toDouble() }.sorted()
                val m = s.size / 2
                val med = if (s.size % 2 == 1) s[m] else (s[m - 1] + s[m]) / 2
                t.ema = t.ema?.let { it + EMA * (med - it) } ?: med
            }
            val e = t.ema ?: continue
            out += Track(name, Math.round(e * 10) / 10.0, t.samples.size)
        }
        out.sortByDescending { it.rssi }
        return out
    }

    // ---- advertise ----

    /** id = the Pulse session id; "" stops. */
    fun setAdvertise(id: String) {
        val hex = id.filter { it in '0'..'9' || it in 'a'..'f' || it in 'A'..'F' }.take(8)
        stopAdvertise()
        if (id.isEmpty()) {
            advertWanted = null
            advertError = ""
        } else if (hex.length < 8) {
            advertWanted = null
            advertError = "session id has no 8 hex characters"
        } else {
            advertWanted = hex
            startAdvertise()
        }
        changed()
    }

    private val advCb = object : AdvertiseCallback() {
        override fun onStartSuccess(settingsInEffect: AdvertiseSettings?) {
            advertising = true
            advertError = ""
            Log.i(Core.TAG, "advertising started: 0xFFFF \"PLS1$advertId\" (mode ${settingsInEffect?.mode}, tx ${settingsInEffect?.txPowerLevel}, connectable ${settingsInEffect?.isConnectable})")
            changed()
        }

        override fun onStartFailure(errorCode: Int) {
            advertising = false
            advertError = when (errorCode) {
                ADVERTISE_FAILED_DATA_TOO_LARGE -> "advert too large"
                ADVERTISE_FAILED_TOO_MANY_ADVERTISERS -> "too many apps advertising"
                ADVERTISE_FAILED_ALREADY_STARTED -> "already started"
                ADVERTISE_FAILED_FEATURE_UNSUPPORTED -> "not supported on this phone"
                else -> "error $errorCode"
            }
            Log.w(Core.TAG, "advertising failed: $advertError")
            changed()
        }
    }

    private fun startAdvertise() {
        val hex = advertWanted ?: return
        advertError = ""
        if (!supported()) { advertError = "this phone has no Bluetooth LE"; return }
        if (!canAdvertise()) { advertError = "permission"; return }
        if (!enabled()) { advertError = "Bluetooth is off"; return }
        val adv = adapter()?.bluetoothLeAdvertiser
        if (adv == null) { advertError = "this phone can't advertise"; return }
        val settings = AdvertiseSettings.Builder()
            .setAdvertiseMode(AdvertiseSettings.ADVERTISE_MODE_LOW_LATENCY)
            .setTxPowerLevel(AdvertiseSettings.ADVERTISE_TX_POWER_HIGH)
            .setConnectable(false)
            .setTimeout(0)
            .build()
        val data = AdvertiseData.Builder()
            .setIncludeDeviceName(false)
            .setIncludeTxPowerLevel(false)
            .addManufacturerData(COMPANY, "PLS1$hex".toByteArray(Charsets.US_ASCII))
            .build()
        try {
            advertId = hex
            adv.startAdvertising(settings, data, advCb)
        } catch (e: Exception) {
            advertError = e.message ?: e.toString()
            Log.w(Core.TAG, "advertise start: $e")
        }
    }

    private fun stopAdvertise() {
        if (advertising || advertId.isNotEmpty()) {
            try { adapter()?.bluetoothLeAdvertiser?.stopAdvertising(advCb) } catch (e: Exception) { Log.w(Core.TAG, "advertise stop: $e") }
            if (advertising) Log.i(Core.TAG, "advertising stopped")
        }
        advertising = false
        advertId = ""
    }

    // ---- Bluetooth switched on/off, or a permission just granted ----

    fun onAdapterChanged() {
        if (!enabled()) {
            scanning = false
            advertising = false
            if (scanWanted) scanError = "Bluetooth is off"
            if (advertWanted != null) advertError = "Bluetooth is off"
        } else {
            retry()
        }
        changed()
    }

    /** Start whatever is wanted and not running. */
    fun retry() {
        if (scanWanted && !scanning) startScan()
        if (advertWanted != null && !advertising) startAdvertise()
        changed()
    }
}
