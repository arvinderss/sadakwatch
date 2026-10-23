package io.sadakwatch.app

import org.json.JSONObject
import java.io.File
import java.util.concurrent.atomic.AtomicLong

/**
 * Crash-safe on-device queue: one JSON file per pending event, written to a
 * temp file and atomically renamed. Mirrors internal/queue on the Go edge agent.
 */
class EventQueue(root: File, private val maxItems: Int = 5000) {
    private val dir = File(root, "queue").apply { mkdirs() }
    val mediaDir = File(root, "queue-media").apply { mkdirs() }
    private val seq = AtomicLong(0)

    data class Item(val file: File, val event: RoadEvent, val imagePath: String?, val attempts: Int)

    init {
        dir.listFiles { f -> f.name.startsWith(".tmp") }?.forEach { it.delete() }
    }

    @Synchronized
    fun enqueue(event: RoadEvent, imagePath: String?): File {
        val name = String.format("%020d-%06d.json", System.currentTimeMillis() * 1_000_000L, seq.incrementAndGet() % 1_000_000)
        val target = File(dir, name)
        write(target, event, imagePath, 0)
        evict()
        return target
    }

    private fun write(target: File, event: RoadEvent, imagePath: String?, attempts: Int) {
        val body = JSONObject().apply {
            put("event", event.toJson())
            if (imagePath != null) put("image_path", imagePath)
            put("attempts", attempts)
        }.toString()
        val tmp = File(dir, ".tmp-" + target.name)
        tmp.outputStream().use { out ->
            out.write(body.toByteArray())
            out.fd.sync()
        }
        if (!tmp.renameTo(target)) {
            tmp.delete()
            throw java.io.IOException("could not commit queue item ${target.name}")
        }
    }

    @Synchronized
    fun update(item: Item, event: RoadEvent, imagePath: String?) {
        if (item.file.exists()) write(item.file, event, imagePath, item.attempts + 1)
    }

    @Synchronized
    fun peek(n: Int): List<Item> {
        val files = dir.listFiles { f -> f.isFile && f.name.endsWith(".json") }?.sortedBy { it.name } ?: return emptyList()
        val out = ArrayList<Item>()
        for (f in files) {
            if (out.size >= n) break
            try {
                val o = JSONObject(f.readText())
                out.add(
                    Item(
                        f,
                        RoadEvent.fromJson(o.getJSONObject("event")),
                        o.optString("image_path", "").ifEmpty { null },
                        o.optInt("attempts", 0),
                    )
                )
            } catch (e: Exception) {
                f.renameTo(File(dir, f.name + ".bad"))
            }
        }
        return out
    }

    @Synchronized
    fun ack(item: Item) {
        item.imagePath?.let { File(it).delete() }
        item.file.delete()
    }

    @Synchronized
    fun size(): Int = dir.listFiles { f -> f.isFile && f.name.endsWith(".json") }?.size ?: 0

    private fun evict() {
        val files = dir.listFiles { f -> f.isFile && f.name.endsWith(".json") }?.sortedBy { it.name } ?: return
        if (files.size <= maxItems) return
        for (f in files.take(files.size - maxItems)) {
            runCatching { JSONObject(f.readText()).optString("image_path", "") }
                .getOrNull()?.takeIf { it.isNotEmpty() }?.let { File(it).delete() }
            f.delete()
        }
    }
}
