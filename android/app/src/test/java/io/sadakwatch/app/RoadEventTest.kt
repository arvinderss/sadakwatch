package io.sadakwatch.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

class RoadEventTest {
    @Test
    fun jsonRoundTripMatchesApiContract() {
        val e = RoadEvent(
            vehicleId = "phone-1", eventType = "pothole", severity = "high", confidence = 0.7,
            latitude = 28.6139, longitude = 77.209, accuracyM = 4.5, detectedAtMillis = 1_792_000_000_123L,
            speedKph = 36.0, headingDegrees = 182.0, source = "accelerometer",
        )
        val json = e.toJson()
        assertEquals("2026-10-14T17:46:40.123Z", json.getString("detected_at"))
        assertFalse(json.has("image_uri"))
        assertEquals(e, RoadEvent.fromJson(json))
    }

    @Test
    fun outOfRangeTelemetryIsClamped() {
        val e = RoadEvent(
            vehicleId = "v", eventType = "debris", severity = "unknown", confidence = 0.9,
            latitude = 1.0, longitude = 1.0, accuracyM = 9000.0, detectedAtMillis = 0,
            speedKph = -3.0, headingDegrees = 400.0, source = "manual",
        ).toJson()
        assertEquals(500.0, e.getDouble("accuracy_m"), 0.0)
        assertEquals(0.0, e.getDouble("speed_kph"), 0.0)
        assertEquals(360.0, e.getDouble("heading_degrees"), 0.0)
    }
}
