package io.sadakwatch.app

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.content.res.ColorStateList
import android.hardware.Sensor
import android.hardware.SensorEvent
import android.hardware.SensorEventListener
import android.hardware.SensorManager
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.util.Size
import android.view.View
import android.view.WindowManager
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.Preview
import androidx.camera.core.resolutionselector.ResolutionSelector
import androidx.camera.core.resolutionselector.ResolutionStrategy
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.core.content.ContextCompat
import com.google.android.material.button.MaterialButton
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import kotlin.math.roundToInt

class MainActivity : AppCompatActivity(), SensorEventListener {

    private lateinit var app: SadakApp
    private lateinit var tracker: LocationTracker
    private lateinit var sensors: SensorManager
    private val detector = BumpDetector()
    private val frames = FrameBuffer(keepMs = 2500, everyMs = 500)

    private lateinit var cameraExecutor: ExecutorService
    private lateinit var ioExecutor: ExecutorService
    private var cameraProvider: ProcessCameraProvider? = null

    private lateinit var preview: PreviewView
    private lateinit var cameraOff: TextView
    private lateinit var statusDot: View
    private lateinit var statusText: TextView
    private lateinit var speedValue: TextView
    private lateinit var gpsValue: TextView
    private lateinit var tripValue: TextView
    private lateinit var queueValue: TextView
    private lateinit var lastEvent: TextView
    private lateinit var driveButton: MaterialButton

    private var driving = false
    private var tripCount = 0
    private val ui = Handler(Looper.getMainLooper())
    private val tick = object : Runnable {
        override fun run() {
            renderHud()
            ui.postDelayed(this, 1000)
        }
    }

    private val permissionRequest =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { granted ->
            if (granted[Manifest.permission.ACCESS_FINE_LOCATION] == true) {
                startDrive()
            } else {
                MaterialAlertDialogBuilder(this)
                    .setTitle("Location needed")
                    .setMessage(getString(R.string.permissions_needed))
                    .setPositiveButton("OK", null)
                    .show()
            }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        app = application as SadakApp
        tracker = LocationTracker(this)
        sensors = getSystemService(SENSOR_SERVICE) as SensorManager
        cameraExecutor = Executors.newSingleThreadExecutor()
        ioExecutor = Executors.newSingleThreadExecutor()

        preview = findViewById(R.id.preview)
        cameraOff = findViewById(R.id.cameraOff)
        statusDot = findViewById(R.id.statusDot)
        statusText = findViewById(R.id.statusText)
        speedValue = findViewById(R.id.speedValue)
        gpsValue = findViewById(R.id.gpsValue)
        tripValue = findViewById(R.id.tripValue)
        queueValue = findViewById(R.id.queueValue)
        lastEvent = findViewById(R.id.lastEvent)
        driveButton = findViewById(R.id.driveButton)

        driveButton.setOnClickListener { if (driving) stopDrive() else requestStart() }
        findViewById<MaterialButton>(R.id.settingsButton).setOnClickListener {
            startActivity(Intent(this, SettingsActivity::class.java))
        }
        findViewById<MaterialButton>(R.id.mapButton).setOnClickListener { openDashboard() }
        findViewById<MaterialButton>(R.id.reportPothole).setOnClickListener { manualReport("pothole") }
        findViewById<MaterialButton>(R.id.reportWater).setOnClickListener { manualReport("waterlogging") }
        findViewById<MaterialButton>(R.id.reportMore).setOnClickListener { chooseReportType() }

        if (!app.settings.isConfigured) {
            lastEvent.text = "Set your SadakWatch server in Settings. Reports are kept on the phone until then."
        }
    }

    override fun onStart() {
        super.onStart()
        app.onQueueChanged = { renderHud() }
        detector.sensitivity = app.settings.sensitivity
        ui.post(tick)
    }

    override fun onStop() {
        super.onStop()
        app.onQueueChanged = null
        ui.removeCallbacks(tick)
        // The camera and sensors are only used while the app is on screen.
        if (driving) {
            stopDrive()
            lastEvent.text = "Drive paused because the app left the screen. Tap Start drive to continue."
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        cameraExecutor.shutdown()
        ioExecutor.shutdown()
        frames.clear()
    }

    // ---------------- drive lifecycle ----------------

    private fun requestStart() {
        val needed = listOf(Manifest.permission.ACCESS_FINE_LOCATION, Manifest.permission.CAMERA)
            .filter { ContextCompat.checkSelfPermission(this, it) != PackageManager.PERMISSION_GRANTED }
        if (needed.isEmpty()) startDrive() else permissionRequest.launch(needed.toTypedArray())
    }

    private fun startDrive() {
        if (driving) return
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.ACCESS_FINE_LOCATION) != PackageManager.PERMISSION_GRANTED) return
        driving = true
        tripCount = 0
        detector.sensitivity = app.settings.sensitivity
        tracker.start()
        if (!tracker.gpsEnabled) {
            Toast.makeText(this, "Turn on device location (GPS) for accurate reports", Toast.LENGTH_LONG).show()
        }
        sensors.getDefaultSensor(Sensor.TYPE_ACCELEROMETER)?.let {
            sensors.registerListener(this, it, SensorManager.SENSOR_DELAY_GAME)
        }
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
            bindCamera()
        }
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        driveButton.text = getString(R.string.stop_drive)
        driveButton.backgroundTintList = ColorStateList.valueOf(ContextCompat.getColor(this, R.color.critical))
        lastEvent.text = "Driving. Bumps are detected automatically; tap a button to report what you see."
        renderHud()
    }

    private fun stopDrive() {
        if (!driving) return
        driving = false
        sensors.unregisterListener(this)
        tracker.stop()
        cameraProvider?.unbindAll()
        frames.clear()
        preview.visibility = View.INVISIBLE
        cameraOff.visibility = View.VISIBLE
        window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        driveButton.text = getString(R.string.start_drive)
        driveButton.backgroundTintList = ColorStateList.valueOf(ContextCompat.getColor(this, R.color.accent))
        lastEvent.text = "Drive ended with $tripCount report${if (tripCount == 1) "" else "s"}."
        UploadWorker.kick(this)
        renderHud()
    }

    private fun bindCamera() {
        val future = ProcessCameraProvider.getInstance(this)
        future.addListener({
            if (!driving) return@addListener
            try {
                val provider = future.get()
                cameraProvider = provider
                val previewUseCase = Preview.Builder().build()
                previewUseCase.setSurfaceProvider(preview.surfaceProvider)
                val analysis = ImageAnalysis.Builder()
                    .setResolutionSelector(
                        ResolutionSelector.Builder()
                            .setResolutionStrategy(
                                ResolutionStrategy(Size(960, 540), ResolutionStrategy.FALLBACK_RULE_CLOSEST_LOWER_THEN_HIGHER)
                            )
                            .build()
                    )
                    .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                    .setOutputImageFormat(ImageAnalysis.OUTPUT_IMAGE_FORMAT_RGBA_8888)
                    .build()
                analysis.setAnalyzer(cameraExecutor, frames)
                provider.unbindAll()
                provider.bindToLifecycle(this, CameraSelector.DEFAULT_BACK_CAMERA, previewUseCase, analysis)
                preview.visibility = View.VISIBLE
                cameraOff.visibility = View.GONE
            } catch (e: Exception) {
                Log.w(TAG, "camera unavailable", e)
                cameraOff.text = "Camera unavailable — reports will be sent without photos."
            }
        }, ContextCompat.getMainExecutor(this))
    }

    // ---------------- detection ----------------

    override fun onSensorChanged(event: SensorEvent) {
        if (!driving || event.sensor.type != Sensor.TYPE_ACCELEROMETER) return
        val bump = detector.feed(
            event.timestamp,
            event.values[0].toDouble(), event.values[1].toDouble(), event.values[2].toDouble()
        ) ?: return
        val loc = tracker.fresh(3000) ?: return
        val kph = if (loc.hasSpeed()) loc.speed * 3.6 else 0.0
        if (kph < app.settings.minSpeedKph) return
        // At speed the defect left the camera's view ~1 s before the wheel reached it.
        val lookback = if (kph > 0) (1100 + 4000 / kph).toLong().coerceAtMost(2200) else 1200L
        report(bump.eventType, bump.severity, bump.confidence, "accelerometer", lookback)
    }

    override fun onAccuracyChanged(sensor: Sensor?, accuracy: Int) {}

    private fun manualReport(type: String) {
        if (!driving) {
            Toast.makeText(this, "Start a drive first so the report has a GPS position", Toast.LENGTH_SHORT).show()
            return
        }
        report(type, "unknown", 0.9, "manual", 0)
    }

    private fun chooseReportType() {
        val types = RoadEvent.TYPES.keys.toList()
        MaterialAlertDialogBuilder(this)
            .setTitle("What do you see?")
            .setItems(types.map { RoadEvent.label(it) }.toTypedArray()) { _, which -> manualReport(types[which]) }
            .show()
    }

    private fun report(type: String, severity: String, confidence: Double, source: String, lookbackMs: Long) {
        val loc = tracker.fresh(5000)
        if (loc == null) {
            lastEvent.text = "Waiting for GPS — report not recorded."
            return
        }
        val event = RoadEvent(
            vehicleId = app.settings.vehicleId,
            eventType = type,
            severity = severity,
            confidence = confidence,
            latitude = loc.latitude,
            longitude = loc.longitude,
            accuracyM = if (loc.hasAccuracy()) loc.accuracy.toDouble() else 0.0,
            detectedAtMillis = System.currentTimeMillis(),
            speedKph = if (loc.hasSpeed()) loc.speed * 3.6 else 0.0,
            headingDegrees = if (loc.hasBearing()) loc.bearing.toDouble() else 0.0,
            source = source,
        )
        tripCount++
        val sev = if (severity == "unknown") "" else " · $severity"
        lastEvent.text = "${RoadEvent.label(type)}$sev recorded at ${"%.5f".format(loc.latitude)}, ${"%.5f".format(loc.longitude)}"
        renderHud()
        val withPhoto = app.settings.attachPhotos
        ioExecutor.execute {
            val image = if (withPhoto) frames.saveJpeg(app.queue.mediaDir, event.eventId, lookbackMs) else null
            try {
                app.queue.enqueue(event, image?.absolutePath)
            } catch (e: Exception) {
                Log.e(TAG, "could not queue report", e)
                image?.delete()
            }
            app.notifyQueueChanged()
            UploadWorker.kick(applicationContext)
        }
    }

    // ---------------- UI ----------------

    private fun renderHud() {
        val loc = if (driving) tracker.fresh(5000) else null
        speedValue.text = if (loc != null && loc.hasSpeed()) (loc.speed * 3.6).roundToInt().toString() else "–"
        gpsValue.text = if (loc != null && loc.hasAccuracy()) loc.accuracy.roundToInt().toString() else "–"
        tripValue.text = tripCount.toString()
        val queued = app.queue.size()
        queueValue.text = queued.toString()

        val (color, text) = when {
            !driving -> R.color.muted to (if (queued > 0) "Ready · $queued waiting to upload" else "Ready")
            loc == null -> R.color.accent to "Driving · waiting for GPS"
            app.lastUploadError != null && queued > 0 -> R.color.accent to "Driving · offline, reports saved"
            else -> R.color.ok to "Driving · recording"
        }
        statusDot.backgroundTintList = ColorStateList.valueOf(ContextCompat.getColor(this, color))
        statusText.text = text
    }

    private fun openDashboard() {
        if (!app.settings.isConfigured) {
            startActivity(Intent(this, SettingsActivity::class.java))
            return
        }
        try {
            startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(app.settings.apiUrl + "/")))
        } catch (e: Exception) {
            Toast.makeText(this, "No browser available", Toast.LENGTH_SHORT).show()
        }
    }

    companion object {
        private const val TAG = "SadakWatch"
    }
}
