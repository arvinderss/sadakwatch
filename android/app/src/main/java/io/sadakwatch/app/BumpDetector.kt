package io.sadakwatch.app

import kotlin.math.abs
import kotlin.math.exp
import kotlin.math.max
import kotlin.math.min
import kotlin.math.sqrt

/**
 * Detects road impacts from raw accelerometer samples, independent of how the
 * phone is mounted.
 *
 * Gravity is tracked with a low-pass filter; the vertical component of the
 * remaining (linear) acceleration is what a wheel transmits when it drops
 * into a pothole or climbs a speed breaker. A spike above the threshold is
 * classified by the sign of the first large excursion around it:
 *  - downward first (wheel drops, then impact)  -> pothole
 *  - upward first (wheel is pushed up, then falls) -> speed_breaker
 *
 * This is a heuristic signal; the server deduplicates and corroborates it
 * with reports from other vehicles.
 */
class BumpDetector(var sensitivity: Sensitivity = Sensitivity.MEDIUM) {

    enum class Sensitivity(val thresholdMs2: Double) { LOW(6.0), MEDIUM(4.5), HIGH(3.2) }

    data class Bump(
        val timestampNanos: Long,
        val eventType: String,
        val severity: String,
        val confidence: Double,
        val peakMs2: Double,
    )

    private val gravity = DoubleArray(3)
    private var gravityReady = false
    private var lastT = 0L
    private var warmupUntil = 0L

    private val times = LongArray(BUFFER)
    private val values = DoubleArray(BUFFER)
    private var head = 0
    private var count = 0

    private var triggerAt = -1L
    private var refractoryUntil = 0L

    /** Feed one accelerometer sample (m/s², device axes). Returns a bump when one completes. */
    fun feed(tNanos: Long, x: Double, y: Double, z: Double): Bump? {
        if (!gravityReady) {
            gravity[0] = x; gravity[1] = y; gravity[2] = z
            gravityReady = true
            lastT = tNanos
            warmupUntil = tNanos + WARMUP_NS
            return null
        }
        val dt = max(0.0, min(0.1, (tNanos - lastT) / 1e9))
        lastT = tNanos
        // Exponential low-pass with a ~0.8 s time constant follows slow tilt
        // (braking, hills, mount sag) but not sub-second impacts.
        val alpha = 1 - exp(-dt / GRAVITY_TAU_S)
        gravity[0] += alpha * (x - gravity[0])
        gravity[1] += alpha * (y - gravity[1])
        gravity[2] += alpha * (z - gravity[2])
        val gNorm = sqrt(gravity[0] * gravity[0] + gravity[1] * gravity[1] + gravity[2] * gravity[2])
        if (gNorm < 1e-3) return null
        val lx = x - gravity[0]
        val ly = y - gravity[1]
        val lz = z - gravity[2]
        val vertical = (lx * gravity[0] + ly * gravity[1] + lz * gravity[2]) / gNorm

        times[head] = tNanos
        values[head] = vertical
        head = (head + 1) % BUFFER
        if (count < BUFFER) count++

        if (tNanos < warmupUntil) return null
        val threshold = sensitivity.thresholdMs2
        if (triggerAt < 0 && tNanos >= refractoryUntil && abs(vertical) >= threshold) {
            triggerAt = tNanos
        }
        if (triggerAt >= 0 && tNanos - triggerAt >= POST_WINDOW_NS) {
            val bump = classify(triggerAt, threshold)
            triggerAt = -1
            refractoryUntil = tNanos + REFRACTORY_NS
            return bump
        }
        return null
    }

    private fun classify(trigger: Long, threshold: Double): Bump {
        val from = trigger - PRE_WINDOW_NS
        var peak = 0.0
        var peakT = trigger
        var firstSign = 0
        // Walk the ring buffer oldest to newest.
        for (i in 0 until count) {
            val idx = (head - count + i + BUFFER) % BUFFER
            val t = times[idx]
            if (t < from) continue
            val v = values[idx]
            if (firstSign == 0 && abs(v) >= threshold * 0.5) firstSign = if (v < 0) -1 else 1
            if (abs(v) > peak) {
                peak = abs(v)
                peakT = t
            }
        }
        val ratio = peak / threshold
        val severity = when {
            ratio >= 2.6 -> "critical"
            ratio >= 1.9 -> "high"
            ratio >= 1.35 -> "medium"
            else -> "low"
        }
        val type = if (firstSign > 0) "speed_breaker" else "pothole"
        val confidence = min(0.85, 0.45 + 0.1 * ratio)
        return Bump(peakT, type, severity, confidence, peak)
    }

    companion object {
        private const val BUFFER = 256 // ~1.3 s at 200 Hz, ~5 s at 50 Hz
        private const val GRAVITY_TAU_S = 0.8
        private const val WARMUP_NS = 2_000_000_000L
        private const val PRE_WINDOW_NS = 400_000_000L
        private const val POST_WINDOW_NS = 250_000_000L
        private const val REFRACTORY_NS = 1_500_000_000L
    }
}
