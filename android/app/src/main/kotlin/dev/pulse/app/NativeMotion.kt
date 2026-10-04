package dev.pulse.app

import android.content.Context
import android.hardware.Sensor
import android.hardware.SensorEvent
import android.hardware.SensorEventListener
import android.hardware.SensorManager
import android.os.Handler
import android.os.HandlerThread
import android.util.Log
import java.util.Locale
import kotlin.math.cos
import kotlin.math.max
import kotlin.math.min
import kotlin.math.sqrt

/**
 * The page's motion stream, in native code, for when the page is paused.
 *
 * Same numbers as web/phone/src/main.ts: every 100 ms one "m" message with the
 * mean acceleration without gravity per axis (m/s², 3 decimals), the largest
 * rotation rate (deg/s) and, when it changed by more than 3° or a second has
 * passed, "g": the unit vector pointing down in the phone's own axes.
 *
 * Chrome's devicemotion on Android is built from the same sensors:
 * acceleration = TYPE_LINEAR_ACCELERATION, accelerationIncludingGravity =
 * TYPE_ACCELEROMETER (so their difference = TYPE_GRAVITY, which points up),
 * rotationRate = TYPE_GYROSCOPE in deg/s.
 */
class NativeMotion(ctx: Context, private val link: NativeLink) : SensorEventListener {
    private val sm = ctx.getSystemService(Context.SENSOR_SERVICE) as SensorManager
    private val thread = HandlerThread("pulse-motion")
    private lateinit var handler: Handler

    private var sx = 0.0; private var sy = 0.0; private var sz = 0.0; private var n = 0
    private var gx = 0.0; private var gy = 0.0; private var gz = 0.0; private var gn = 0
    private var rot = 0.0
    private var fused = true
    private var grav: DoubleArray? = null // running mean, only without a linear-acceleration sensor
    private var lastAccelNs = 0L

    private var sentG: DoubleArray? = null
    private var sentGAt = 0L
    private var sentGConn = -1
    private val resendCos = cos(3 * Math.PI / 180)

    private val flush = object : Runnable {
        override fun run() {
            handler.postDelayed(this, 100)
            flushNow()
        }
    }

    fun start() {
        thread.start()
        handler = Handler(thread.looper)
        val lin = sm.getDefaultSensor(Sensor.TYPE_LINEAR_ACCELERATION)
        val gravity = sm.getDefaultSensor(Sensor.TYPE_GRAVITY)
        val gyro = sm.getDefaultSensor(Sensor.TYPE_GYROSCOPE)
        fused = lin != null && gravity != null
        val rate = SensorManager.SENSOR_DELAY_GAME // ~50 Hz
        if (fused) {
            sm.registerListener(this, lin, rate, handler)
            sm.registerListener(this, gravity, rate, handler)
        } else {
            sm.registerListener(this, sm.getDefaultSensor(Sensor.TYPE_ACCELEROMETER), rate, handler)
        }
        if (gyro != null) sm.registerListener(this, gyro, rate, handler)
        handler.postDelayed(flush, 100)
        Log.i(Core.TAG, "native motion: ${if (fused) "linear acceleration + gravity" else "accelerometer minus running mean"}${if (gyro != null) " + gyroscope" else ", no gyroscope"}")
    }

    fun stop() {
        sm.unregisterListener(this)
        thread.quitSafely()
    }

    override fun onAccuracyChanged(sensor: Sensor?, accuracy: Int) {}

    override fun onSensorChanged(e: SensorEvent) {
        val v = e.values
        when (e.sensor.type) {
            Sensor.TYPE_LINEAR_ACCELERATION -> { sx += v[0]; sy += v[1]; sz += v[2]; n++ }
            Sensor.TYPE_GRAVITY -> { gx += v[0]; gy += v[1]; gz += v[2]; gn++ }
            Sensor.TYPE_GYROSCOPE -> rot = max(rot, sqrt((v[0] * v[0] + v[1] * v[1] + v[2] * v[2]).toDouble()) * 180 / Math.PI)
            Sensor.TYPE_ACCELEROMETER -> {
                // No fused sensors: subtract a running mean (about 1 s), as the page does.
                val dt = if (lastAccelNs == 0L) 0.02 else (e.timestamp - lastAccelNs) / 1e9
                lastAccelNs = e.timestamp
                val k = min(1.0, dt)
                val g = grav ?: doubleArrayOf(v[0].toDouble(), v[1].toDouble(), v[2].toDouble()).also { grav = it }
                for (i in 0..2) g[i] += k * (v[i] - g[i])
                sx += v[0] - g[0]; sy += v[1] - g[1]; sz += v[2] - g[2]; n++
                gx += g[0]; gy += g[1]; gz += g[2]; gn++
            }
        }
    }

    private fun flushNow() {
        if (n == 0) return
        val t = System.currentTimeMillis()
        val sb = StringBuilder(96)
        sb.append(String.format(Locale.US, "{\"type\":\"m\",\"t\":%d,\"ax\":%.3f,\"ay\":%.3f,\"az\":%.3f,\"rot\":%.3f", t, sx / n, sy / n, sz / n, rot))
        // Down = minus the gravity reaction (it points up on Android). Sent first thing on every
        // connection, when it turned more than 3°, and once a second.
        if (gn > 0 && link.open) {
            val norm = sqrt(gx * gx + gy * gy + gz * gz) / gn
            if (norm > 4 && norm < 16) {
                val k = -1.0 / (norm * gn)
                val g = doubleArrayOf(r2(gx * k), r2(gy * k), r2(gz * k))
                val s = sentG
                val conn = link.connects.get()
                val dot = if (s != null) g[0] * s[0] + g[1] * s[1] + g[2] * s[2] else -1.0
                if (s == null || conn != sentGConn || dot < resendCos || t - sentGAt >= 1000) {
                    sb.append(String.format(Locale.US, ",\"g\":[%.2f,%.2f,%.2f]", g[0], g[1], g[2]))
                    sentG = g
                    sentGAt = t
                    sentGConn = conn
                }
            }
        }
        sb.append('}')
        sx = 0.0; sy = 0.0; sz = 0.0; n = 0
        gx = 0.0; gy = 0.0; gz = 0.0; gn = 0
        rot = 0.0
        link.sendMotion(sb.toString())
    }

    private fun r2(v: Double) = Math.round(v * 100) / 100.0 + 0.0
}
