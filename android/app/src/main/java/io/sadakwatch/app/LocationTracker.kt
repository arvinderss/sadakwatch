package io.sadakwatch.app

import android.annotation.SuppressLint
import android.content.Context
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.os.Bundle
import android.os.Looper
import android.os.SystemClock

/** Follows the GNSS receiver using the platform LocationManager (no Play Services). */
class LocationTracker(context: Context) : LocationListener {
    private val manager = context.getSystemService(Context.LOCATION_SERVICE) as LocationManager

    @Volatile
    var latest: Location? = null
        private set

    var onUpdate: ((Location) -> Unit)? = null

    @SuppressLint("MissingPermission") // callers check the permission first
    fun start() {
        if (manager.isProviderEnabled(LocationManager.GPS_PROVIDER)) {
            manager.requestLocationUpdates(LocationManager.GPS_PROVIDER, 1000L, 0f, this, Looper.getMainLooper())
        }
        // Network fixes help indoors/at start-up; GPS fixes replace them as soon as they arrive.
        if (manager.isProviderEnabled(LocationManager.NETWORK_PROVIDER)) {
            manager.requestLocationUpdates(LocationManager.NETWORK_PROVIDER, 5000L, 0f, this, Looper.getMainLooper())
        }
    }

    fun stop() {
        manager.removeUpdates(this)
    }

    val gpsEnabled: Boolean get() = manager.isProviderEnabled(LocationManager.GPS_PROVIDER)

    /** The latest fix if it is younger than [maxAgeMs]. */
    fun fresh(maxAgeMs: Long = 5000): Location? {
        val l = latest ?: return null
        val ageMs = (SystemClock.elapsedRealtimeNanos() - l.elapsedRealtimeNanos) / 1_000_000
        return if (ageMs <= maxAgeMs) l else null
    }

    override fun onLocationChanged(location: Location) {
        val current = latest
        // Don't let a coarse network fix overwrite a recent GPS fix.
        if (current != null && location.provider == LocationManager.NETWORK_PROVIDER &&
            current.provider == LocationManager.GPS_PROVIDER &&
            (location.elapsedRealtimeNanos - current.elapsedRealtimeNanos) < 10_000_000_000L
        ) return
        latest = location
        onUpdate?.invoke(location)
    }

    @Deprecated("Deprecated in Java")
    override fun onStatusChanged(provider: String?, status: Int, extras: Bundle?) {}
    override fun onProviderEnabled(provider: String) {}
    override fun onProviderDisabled(provider: String) {}
}
