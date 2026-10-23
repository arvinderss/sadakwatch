package io.sadakwatch.app

import android.app.Application
import android.os.Handler
import android.os.Looper

class SadakApp : Application() {
    lateinit var settings: Settings
        private set
    lateinit var queue: EventQueue
        private set

    @Volatile
    var lastUploadError: String? = null

    /** Observed by the UI to refresh the "to upload" counter. */
    var onQueueChanged: (() -> Unit)? = null
    private val main = Handler(Looper.getMainLooper())

    override fun onCreate() {
        super.onCreate()
        settings = Settings(this)
        queue = EventQueue(filesDir)
        UploadWorker.schedulePeriodic(this)
        if (queue.size() > 0) UploadWorker.kick(this)
    }

    fun notifyQueueChanged() {
        main.post { onQueueChanged?.invoke() }
    }
}
