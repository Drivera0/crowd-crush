package dev.pulse.app

import android.util.Log
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong

/**
 * The phone's connection to /ws/phone while the page can't hold it (screen off).
 * Same protocol as the page: hello with the page's session id, pong for every
 * ping (clock sync), then "m" (and "beacons") messages. The server lets the
 * newest socket of an id win, so the page takes the session back just by
 * reconnecting when it is visible again.
 */
class NativeLink(private val url: String, private val hello: String, private val onState: (JSONObject) -> Unit) {
    private val client = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS)
        .pingInterval(15, TimeUnit.SECONDS)
        .build()

    @Volatile
    private var ws: WebSocket? = null
    @Volatile
    private var closed = false
    @Volatile
    var open = false
        private set
    /** Counts connections: a new one means "send the gravity vector again". */
    val connects = AtomicInteger()
    /** Motion readings sent. */
    val sent = AtomicLong()
    private var backoff = 500L

    fun start() = connect()

    private fun connect() {
        if (closed) return
        ws = client.newWebSocket(Request.Builder().url(url).build(), listener)
    }

    private val listener = object : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            webSocket.send(hello)
            backoff = 500
            connects.incrementAndGet()
            open = true
            Log.i(Core.TAG, "native link open")
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            try {
                val m = JSONObject(text)
                when (m.optString("type")) {
                    "ping" -> webSocket.send("{\"type\":\"pong\",\"t0\":${m.getLong("t0")},\"t1\":${System.currentTimeMillis()}}")
                    "state" -> onState(m)
                }
            } catch (e: Exception) {
                /* not for us */
            }
        }

        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            webSocket.close(1000, null)
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = lost(webSocket, "closed $code")

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = lost(webSocket, t.toString())
    }

    private fun lost(sock: WebSocket, why: String) {
        if (sock !== ws) return
        open = false
        if (closed) return
        Log.w(Core.TAG, "native link lost ($why), retrying in $backoff ms")
        Core.main.postDelayed({ connect() }, backoff)
        backoff = minOf(backoff * 2, 5000)
    }

    /** A motion reading. False if the link is down (the reading is dropped, as the page does). */
    fun sendMotion(json: String): Boolean {
        if (!open || ws?.send(json) != true) return false
        sent.incrementAndGet()
        return true
    }

    fun send(json: String): Boolean = open && ws?.send(json) == true

    fun close() {
        closed = true
        open = false
        ws?.close(1000, null)
        client.dispatcher.executorService.shutdown()
    }
}
