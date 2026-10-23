package io.sadakwatch.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.math.sin

class BumpDetectorTest {
    private val hz = 100
    private val stepNs = 1_000_000_000L / hz

    /** Feeds [seconds] of samples; [vertical] gives extra acceleration along gravity at time t (s). */
    private fun run(
        d: BumpDetector,
        seconds: Double,
        startNs: Long = 0,
        // Phone mounted tilted: gravity split across y and z.
        gy: Double = 6.0,
        gz: Double = 7.76,
        vertical: (Double) -> Double,
    ): List<BumpDetector.Bump> {
        val out = ArrayList<BumpDetector.Bump>()
        val n = (seconds * hz).toInt()
        val gNorm = kotlin.math.sqrt(gy * gy + gz * gz)
        for (i in 0 until n) {
            val t = startNs + i * stepNs
            val v = vertical(t / 1e9)
            // Small road-noise vibration on all axes.
            val noise = 0.3 * sin(t / 1e9 * 2 * Math.PI * 13)
            val b = d.feed(t, noise, gy + v * gy / gNorm, gz + v * gz / gNorm)
            if (b != null) out.add(b)
        }
        return out
    }

    @Test
    fun smoothRoadProducesNothing() {
        val bumps = run(BumpDetector(), 10.0) { 0.0 }
        assertTrue(bumps.isEmpty())
    }

    @Test
    fun potholeIsDropThenImpact() {
        val bumps = run(BumpDetector(BumpDetector.Sensitivity.MEDIUM), 6.0) { t ->
            when {
                t in 3.00..3.06 -> -5.0 // wheel drops into the hole
                t in 3.06..3.10 -> 13.0 // hits the far edge
                else -> 0.0
            }
        }
        assertEquals(1, bumps.size)
        assertEquals("pothole", bumps[0].eventType)
        assertEquals("critical", bumps[0].severity)
        assertTrue(bumps[0].confidence in 0.45..0.85)
    }

    @Test
    fun speedBreakerIsLiftFirst() {
        val bumps = run(BumpDetector(BumpDetector.Sensitivity.MEDIUM), 6.0) { t ->
            when {
                t in 3.00..3.08 -> 6.5
                t in 3.08..3.16 -> -4.0
                else -> 0.0
            }
        }
        assertEquals(1, bumps.size)
        assertEquals("speed_breaker", bumps[0].eventType)
        assertEquals("medium", bumps[0].severity)
    }

    @Test
    fun sensitivityChangesTheThreshold() {
        val mild: (Double) -> Double = { t -> if (t in 3.0..3.05) -4.0 else 0.0 }
        assertTrue(run(BumpDetector(BumpDetector.Sensitivity.LOW), 6.0, vertical = mild).isEmpty())
        assertEquals(1, run(BumpDetector(BumpDetector.Sensitivity.HIGH), 6.0, vertical = mild).size)
    }

    @Test
    fun refractoryPeriodMergesRattle() {
        val bumps = run(BumpDetector(), 8.0) { t ->
            if ((t in 3.0..3.04) || (t in 3.5..3.54) || (t in 6.0..6.04)) -9.0 else 0.0
        }
        // The rattle 0.5 s after the first hit is ignored; the one 3 s later is new.
        assertEquals(2, bumps.size)
    }

    @Test
    fun warmupIgnoresMountingShake() {
        val d = BumpDetector()
        val bumps = run(d, 1.5) { t -> if (t in 0.5..0.55) 15.0 else 0.0 }
        assertTrue(bumps.isEmpty())
        assertNull(d.feed(2_000_000_000L, 0.0, 6.0, 7.76))
        assertNotNull(d)
    }
}
