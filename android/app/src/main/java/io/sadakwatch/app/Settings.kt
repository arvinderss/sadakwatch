package io.sadakwatch.app

import android.content.Context
import android.content.SharedPreferences
import java.util.UUID

/** Persistent user configuration. */
class Settings(context: Context) {
    private val prefs: SharedPreferences =
        context.applicationContext.getSharedPreferences("sadakwatch", Context.MODE_PRIVATE)

    var apiUrl: String
        get() = prefs.getString(KEY_API_URL, "")!!.trim().trimEnd('/')
        set(v) = prefs.edit().putString(KEY_API_URL, v.trim().trimEnd('/')).apply()

    var apiKey: String
        get() = prefs.getString(KEY_API_KEY, "")!!
        set(v) = prefs.edit().putString(KEY_API_KEY, v.trim()).apply()

    /** Stable per-install vehicle identifier unless the user sets one. */
    var vehicleId: String
        get() {
            val existing = prefs.getString(KEY_VEHICLE_ID, null)
            if (!existing.isNullOrBlank()) return existing
            val generated = "phone-" + UUID.randomUUID().toString().substring(0, 8)
            prefs.edit().putString(KEY_VEHICLE_ID, generated).apply()
            return generated
        }
        set(v) = prefs.edit().putString(KEY_VEHICLE_ID, v.trim().take(64)).apply()

    var sensitivity: BumpDetector.Sensitivity
        get() = runCatching {
            BumpDetector.Sensitivity.valueOf(prefs.getString(KEY_SENSITIVITY, "MEDIUM")!!)
        }.getOrDefault(BumpDetector.Sensitivity.MEDIUM)
        set(v) = prefs.edit().putString(KEY_SENSITIVITY, v.name).apply()

    var minSpeedKph: Int
        get() = prefs.getInt(KEY_MIN_SPEED, 12)
        set(v) = prefs.edit().putInt(KEY_MIN_SPEED, v.coerceIn(0, 120)).apply()

    var attachPhotos: Boolean
        get() = prefs.getBoolean(KEY_PHOTOS, true)
        set(v) = prefs.edit().putBoolean(KEY_PHOTOS, v).apply()

    val isConfigured: Boolean get() = apiUrl.startsWith("http://") || apiUrl.startsWith("https://")

    companion object {
        private const val KEY_API_URL = "api_url"
        private const val KEY_API_KEY = "api_key"
        private const val KEY_VEHICLE_ID = "vehicle_id"
        private const val KEY_SENSITIVITY = "sensitivity"
        private const val KEY_MIN_SPEED = "min_speed_kph"
        private const val KEY_PHOTOS = "attach_photos"
    }
}
