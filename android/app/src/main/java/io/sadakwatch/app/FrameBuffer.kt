package io.sadakwatch.app

import android.graphics.Bitmap
import android.graphics.Matrix
import android.os.SystemClock
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import java.io.File
import java.io.FileOutputStream

/**
 * Keeps the last few seconds of camera frames so a report can include the
 * frame in which the defect was still visible ahead of the vehicle (by the
 * time the wheel hits a pothole it is already out of view).
 */
class FrameBuffer(private val keepMs: Long = 3000, private val everyMs: Long = 400) : ImageAnalysis.Analyzer {

    private data class Frame(val atMs: Long, val bitmap: Bitmap, val rotation: Int)

    private val frames = ArrayDeque<Frame>()
    private var lastKeptMs = 0L

    override fun analyze(image: ImageProxy) {
        try {
            val now = SystemClock.elapsedRealtime()
            if (now - lastKeptMs < everyMs) return
            lastKeptMs = now
            val bmp = image.toBitmap()
            synchronized(frames) {
                frames.addLast(Frame(now, bmp, image.imageInfo.rotationDegrees))
                while (frames.isNotEmpty() && now - frames.first().atMs > keepMs) {
                    frames.removeFirst().bitmap.recycle()
                }
            }
        } finally {
            image.close()
        }
    }

    /**
     * Writes the frame captured closest to [lookbackMs] before now as a JPEG.
     * Returns null when no frame is available.
     */
    fun saveJpeg(dir: File, name: String, lookbackMs: Long): File? {
        val target = SystemClock.elapsedRealtime() - lookbackMs
        val frame: Frame
        val copy: Bitmap
        synchronized(frames) {
            frame = frames.minByOrNull { kotlin.math.abs(it.atMs - target) } ?: return null
            if (frame.bitmap.isRecycled) return null
            copy = frame.bitmap.copy(Bitmap.Config.ARGB_8888, false) ?: return null
        }
        val rotated = if (frame.rotation != 0) {
            val m = Matrix().apply { postRotate(frame.rotation.toFloat()) }
            Bitmap.createBitmap(copy, 0, 0, copy.width, copy.height, m, true).also {
                if (it !== copy) copy.recycle()
            }
        } else copy
        dir.mkdirs()
        val out = File(dir, "$name.jpg")
        return try {
            FileOutputStream(out).use { rotated.compress(Bitmap.CompressFormat.JPEG, 80, it) }
            out
        } catch (e: Exception) {
            out.delete()
            null
        } finally {
            rotated.recycle()
        }
    }

    fun clear() {
        synchronized(frames) {
            frames.forEach { it.bitmap.recycle() }
            frames.clear()
        }
    }
}
