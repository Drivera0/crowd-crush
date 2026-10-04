package dev.pulse.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.os.VibrationEffect
import android.os.Vibrator
import android.util.Log

/**
 * Keeps the process, the Bluetooth scan/advert and the native motion stream
 * alive while the screen is off. Its notification is always there while Pulse
 * is running: nothing happens in the background without it.
 */
class PulseService : Service() {
    companion object {
        @Volatile
        var instance: PulseService? = null
        const val ACTION_STOP = "dev.pulse.app.STOP"
        private const val CH_RUN = "running"
        private const val CH_ALERT = "alert"
        private const val ID_RUN = 1
        private const val ID_ALERT = 2
    }

    private val nm by lazy { getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
        nm.createNotificationChannel(NotificationChannel(CH_RUN, "Pulse is running", NotificationManager.IMPORTANCE_LOW).apply {
            description = "Shown the whole time Pulse is running for an event"
        })
        nm.createNotificationChannel(NotificationChannel(CH_ALERT, "Crowd danger", NotificationManager.IMPORTANCE_HIGH).apply {
            description = "Crowd danger near you while the screen is off"
        })
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            Log.i(Core.TAG, "stopped from the notification")
            Core.stopAll()
            stopSelf()
            return START_NOT_STICKY
        }
        try {
            if (Build.VERSION.SDK_INT >= 29) {
                // connectedDevice needs a Bluetooth permission on Android 14; without one the service is plain data sync.
                val bt = Core.ble.canScan() || Core.ble.canAdvertise()
                val type = if (bt) ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE or ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
                else ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
                startForeground(ID_RUN, build(), type)
            } else {
                startForeground(ID_RUN, build())
            }
            Log.i(Core.TAG, "foreground service running")
        } catch (e: Exception) {
            Log.w(Core.TAG, "startForeground: $e")
            stopSelf()
        }
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        instance = null
        nm.cancel(ID_ALERT)
        Log.i(Core.TAG, "foreground service stopped")
        super.onDestroy()
    }

    private fun open(): PendingIntent =
        PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)

    private fun build(): Notification {
        val stop = PendingIntent.getService(this, 1, Intent(this, PulseService::class.java).setAction(ACTION_STOP), PendingIntent.FLAG_IMMUTABLE)
        val b = Notification.Builder(this, CH_RUN)
            .setSmallIcon(R.drawable.ic_stat_pulse)
            .setContentTitle("Pulse is running for this event")
            .setContentText(Core.summary())
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(open())
            .addAction(Notification.Action.Builder(null, "Stop", stop).build())
        if (Build.VERSION.SDK_INT >= 31) b.setForegroundServiceBehavior(Notification.FOREGROUND_SERVICE_IMMEDIATE)
        return b.build()
    }

    /** Re-draw the notification text. */
    fun refresh() {
        runCatching { nm.notify(ID_RUN, build()) }
    }

    /** Crowd danger while the screen is off: a heads-up notification and the same buzz the page gives. */
    fun danger(on: Boolean, text: String) {
        if (!on) {
            nm.cancel(ID_ALERT)
            return
        }
        Log.i(Core.TAG, "danger while the screen is off: $text")
        val n = Notification.Builder(this, CH_ALERT)
            .setSmallIcon(R.drawable.ic_stat_pulse)
            .setContentTitle("Crowd danger near you")
            .setContentText(text)
            .setStyle(Notification.BigTextStyle().bigText(text))
            .setCategory(Notification.CATEGORY_ALARM)
            .setAutoCancel(true)
            .setContentIntent(open())
            .build()
        runCatching { nm.notify(ID_ALERT, n) }
        runCatching {
            @Suppress("DEPRECATION")
            val v = getSystemService(Context.VIBRATOR_SERVICE) as Vibrator
            v.vibrate(VibrationEffect.createWaveform(longArrayOf(0, 300, 120, 300, 120, 600), -1))
        }
    }
}
