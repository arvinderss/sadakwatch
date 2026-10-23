package io.sadakwatch.app

import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.util.UUID

/**
 * Drains the queue to the SadakWatch API: uploads each photo once
 * (POST /v1/media), records the returned URI on the queued item, then posts
 * events in batches (POST /v1/events). Event IDs are generated on the phone,
 * so a retry after a lost response is recognised by the server as a duplicate.
 */
class Uploader(private val settings: Settings, private val queue: EventQueue) {

    class PermanentError(msg: String) : IOException(msg)

    data class Result(val sent: Int, val rejected: Int)

    /** Uploads everything queued. Throws IOException on transport/server errors. */
    fun drain(maxBatches: Int = 40): Result {
        var sent = 0
        var rejected = 0
        repeat(maxBatches) {
            val r = flushBatch(25)
            sent += r.sent
            rejected += r.rejected
            if (r.sent == 0) return Result(sent, rejected)
        }
        return Result(sent, rejected)
    }

    private fun flushBatch(size: Int): Result {
        val items = queue.peek(size)
        if (items.isEmpty()) return Result(0, 0)
        val events = ArrayList<RoadEvent>(items.size)
        for (item in items) {
            var event = item.event
            var image = item.imagePath
            if (image != null && event.imageUri == null) {
                try {
                    val uri = uploadImage(File(image))
                    event = event.copy(imageUri = uri)
                } catch (e: PermanentError) {
                    File(image).delete()
                    image = null // send the report without its photo
                } catch (e: java.io.FileNotFoundException) {
                    image = null
                }
                queue.update(item, event, image)
            }
            events.add(event)
        }
        val results = postEvents(events)
        var rejected = 0
        items.forEachIndexed { i, item ->
            if (results.optJSONObject(i)?.optString("error", "")?.isNotEmpty() == true) rejected++
            queue.ack(item)
        }
        return Result(items.size, rejected)
    }

    private fun open(path: String, method: String): HttpURLConnection {
        if (!settings.isConfigured) throw IOException("Set the server URL in Settings")
        val conn = URL(settings.apiUrl + path).openConnection() as HttpURLConnection
        conn.requestMethod = method
        conn.connectTimeout = 15_000
        conn.readTimeout = 60_000
        conn.doInput = true
        if (settings.apiKey.isNotEmpty()) conn.setRequestProperty("Authorization", "Bearer " + settings.apiKey)
        return conn
    }

    private fun readBody(conn: HttpURLConnection): String {
        val stream = if (conn.responseCode < 400) conn.inputStream else conn.errorStream
        return stream?.bufferedReader()?.use { it.readText() } ?: ""
    }

    private fun uploadImage(file: File): String {
        if (!file.exists()) throw java.io.FileNotFoundException(file.path)
        val boundary = "----sadakwatch" + UUID.randomUUID().toString().replace("-", "")
        val conn = open("/v1/media", "POST")
        try {
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "multipart/form-data; boundary=$boundary")
            conn.outputStream.use { out ->
                out.write("--$boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"${file.name}\"\r\nContent-Type: image/jpeg\r\n\r\n".toByteArray())
                file.inputStream().use { it.copyTo(out) }
                out.write("\r\n--$boundary--\r\n".toByteArray())
            }
            val code = conn.responseCode
            val body = readBody(conn)
            return when {
                code == 201 -> JSONObject(body).getString("uri")
                code == 400 || code == 413 -> throw PermanentError("photo rejected ($code)")
                code == 401 -> throw IOException("API key rejected (401)")
                else -> throw IOException("photo upload failed ($code)")
            }
        } finally {
            conn.disconnect()
        }
    }

    private fun postEvents(events: List<RoadEvent>): JSONArray {
        val payload = JSONObject().put("events", JSONArray().apply { events.forEach { put(it.toJson()) } })
        val conn = open("/v1/events", "POST")
        try {
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
            conn.outputStream.use { it.write(payload.toString().toByteArray()) }
            val code = conn.responseCode
            val body = readBody(conn)
            if (code == 401) throw IOException("API key rejected (401)")
            if (code != 200 && code != 201) throw IOException("event upload failed ($code)")
            return JSONObject(body).optJSONArray("results") ?: JSONArray()
        } finally {
            conn.disconnect()
        }
    }

    /** GET /healthz; returns a human-readable status line. */
    fun ping(): String {
        val conn = open("/healthz", "GET")
        try {
            val code = conn.responseCode
            val body = readBody(conn)
            if (code != 200) throw IOException("server answered $code")
            val version = JSONObject(body).optString("version", "")
            return if (version.isNotEmpty()) "Connected (server $version)" else "Connected"
        } finally {
            conn.disconnect()
        }
    }
}
