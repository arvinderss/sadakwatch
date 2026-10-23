package io.sadakwatch.app

import org.json.JSONObject
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone
import java.util.UUID

/** Mirrors the API's road-event contract (internal/events/event.go). */
data class RoadEvent(
    val eventId: String = UUID.randomUUID().toString(),
    val vehicleId: String,
    val eventType: String,
    val severity: String,
    val confidence: Double,
    val latitude: Double,
    val longitude: Double,
    val accuracyM: Double,
    val detectedAtMillis: Long,
    val speedKph: Double,
    val headingDegrees: Double,
    val source: String,
    val imageUri: String? = null,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("event_id", eventId)
        put("vehicle_id", vehicleId)
        put("event_type", eventType)
        put("severity", severity)
        put("confidence", confidence)
        put("latitude", latitude)
        put("longitude", longitude)
        put("accuracy_m", accuracyM.coerceIn(0.0, 500.0))
        put("detected_at", isoUtc(detectedAtMillis))
        put("speed_kph", speedKph.coerceIn(0.0, 300.0))
        put("heading_degrees", headingDegrees.coerceIn(0.0, 360.0))
        put("source", source)
        if (!imageUri.isNullOrEmpty()) put("image_uri", imageUri)
    }

    companion object {
        val TYPES = linkedMapOf(
            "pothole" to "Pothole",
            "road_crack" to "Road crack",
            "broken_surface" to "Broken surface",
            "waterlogging" to "Waterlogging",
            "debris" to "Debris",
            "road_edge_damage" to "Damaged road edge",
            "missing_lane_marking" to "Missing lane marking",
            "damaged_manhole" to "Damaged manhole",
            "speed_breaker" to "Speed breaker",
        )

        fun label(type: String): String = TYPES[type] ?: type

        fun fromJson(o: JSONObject): RoadEvent = RoadEvent(
            eventId = o.getString("event_id"),
            vehicleId = o.getString("vehicle_id"),
            eventType = o.getString("event_type"),
            severity = o.optString("severity", "unknown"),
            confidence = o.optDouble("confidence", 0.0),
            latitude = o.getDouble("latitude"),
            longitude = o.getDouble("longitude"),
            accuracyM = o.optDouble("accuracy_m", 0.0),
            detectedAtMillis = parseIso(o.getString("detected_at")),
            speedKph = o.optDouble("speed_kph", 0.0),
            headingDegrees = o.optDouble("heading_degrees", 0.0),
            source = o.optString("source", "manual"),
            imageUri = o.optString("image_uri", "").ifEmpty { null },
        )

        private fun formatter() = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.US).apply {
            timeZone = TimeZone.getTimeZone("UTC")
        }

        fun isoUtc(millis: Long): String = formatter().format(Date(millis))

        fun parseIso(s: String): Long = formatter().parse(s)?.time ?: 0L
    }
}
