package io.sadakwatch.app

import android.content.Context
import android.util.Log
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import java.util.concurrent.TimeUnit

/** Uploads queued reports whenever the network is available, with backoff. */
class UploadWorker(context: Context, params: WorkerParameters) : Worker(context, params) {

    override fun doWork(): Result {
        val app = applicationContext as SadakApp
        if (!app.settings.isConfigured) return Result.success()
        return try {
            val r = Uploader(app.settings, app.queue).drain()
            if (r.sent > 0) Log.i(TAG, "uploaded ${r.sent} reports (${r.rejected} rejected)")
            app.lastUploadError = null
            app.notifyQueueChanged()
            if (app.queue.size() > 0) Result.retry() else Result.success()
        } catch (e: Exception) {
            Log.w(TAG, "upload failed: ${e.message}")
            app.lastUploadError = e.message
            app.notifyQueueChanged()
            Result.retry()
        }
    }

    companion object {
        private const val TAG = "SadakWatchUpload"
        private const val ONCE = "sadakwatch-upload-now"
        private const val PERIODIC = "sadakwatch-upload-periodic"

        private val constraints = Constraints.Builder()
            .setRequiredNetworkType(NetworkType.CONNECTED)
            .build()

        /** Upload as soon as possible (debounced by WorkManager's unique work). */
        fun kick(context: Context) {
            val req = OneTimeWorkRequestBuilder<UploadWorker>()
                .setConstraints(constraints)
                .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 15, TimeUnit.SECONDS)
                .build()
            WorkManager.getInstance(context).enqueueUniqueWork(ONCE, ExistingWorkPolicy.APPEND_OR_REPLACE, req)
        }

        /** Safety net that drains anything left over, e.g. after a reboot. */
        fun schedulePeriodic(context: Context) {
            val req = PeriodicWorkRequestBuilder<UploadWorker>(15, TimeUnit.MINUTES)
                .setConstraints(constraints)
                .build()
            WorkManager.getInstance(context).enqueueUniquePeriodicWork(PERIODIC, ExistingPeriodicWorkPolicy.KEEP, req)
        }
    }
}
