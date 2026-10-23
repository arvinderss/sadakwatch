package io.sadakwatch.app

import android.os.Bundle
import android.widget.RadioGroup
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import com.google.android.material.button.MaterialButton
import com.google.android.material.materialswitch.MaterialSwitch
import com.google.android.material.textfield.TextInputEditText
import java.util.concurrent.Executors

class SettingsActivity : AppCompatActivity() {

    private val executor = Executors.newSingleThreadExecutor()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_settings)
        val settings = (application as SadakApp).settings

        val apiUrl = findViewById<TextInputEditText>(R.id.apiUrl)
        val apiKey = findViewById<TextInputEditText>(R.id.apiKey)
        val vehicleId = findViewById<TextInputEditText>(R.id.vehicleId)
        val sensitivity = findViewById<RadioGroup>(R.id.sensitivity)
        val minSpeed = findViewById<TextInputEditText>(R.id.minSpeed)
        val photos = findViewById<MaterialSwitch>(R.id.attachPhotos)
        val testResult = findViewById<TextView>(R.id.testResult)

        apiUrl.setText(settings.apiUrl)
        apiKey.setText(settings.apiKey)
        vehicleId.setText(settings.vehicleId)
        minSpeed.setText(settings.minSpeedKph.toString())
        photos.isChecked = settings.attachPhotos
        sensitivity.check(
            when (settings.sensitivity) {
                BumpDetector.Sensitivity.LOW -> R.id.sensLow
                BumpDetector.Sensitivity.MEDIUM -> R.id.sensMedium
                BumpDetector.Sensitivity.HIGH -> R.id.sensHigh
            }
        )
        findViewById<TextView>(R.id.versionText).text =
            "SadakWatch ${BuildConfig.VERSION_NAME} · reports waiting: ${(application as SadakApp).queue.size()}"

        fun save(): Boolean {
            val url = apiUrl.text?.toString()?.trim().orEmpty()
            if (url.isNotEmpty() && !url.startsWith("http://") && !url.startsWith("https://")) {
                apiUrl.error = "Start with http:// or https://"
                return false
            }
            settings.apiUrl = url
            settings.apiKey = apiKey.text?.toString().orEmpty()
            vehicleId.text?.toString()?.trim()?.takeIf { it.isNotEmpty() }?.let { settings.vehicleId = it }
            settings.minSpeedKph = minSpeed.text?.toString()?.toIntOrNull() ?: settings.minSpeedKph
            settings.attachPhotos = photos.isChecked
            settings.sensitivity = when (sensitivity.checkedRadioButtonId) {
                R.id.sensLow -> BumpDetector.Sensitivity.LOW
                R.id.sensHigh -> BumpDetector.Sensitivity.HIGH
                else -> BumpDetector.Sensitivity.MEDIUM
            }
            return true
        }

        findViewById<MaterialButton>(R.id.testButton).setOnClickListener {
            if (!save()) return@setOnClickListener
            testResult.text = "Checking…"
            val app = application as SadakApp
            executor.execute {
                val msg = try {
                    Uploader(app.settings, app.queue).ping()
                } catch (e: Exception) {
                    "Failed: ${e.message ?: e.javaClass.simpleName}"
                }
                runOnUiThread { testResult.text = msg }
            }
        }

        findViewById<MaterialButton>(R.id.saveButton).setOnClickListener {
            if (save()) {
                UploadWorker.kick(this)
                Toast.makeText(this, "Saved", Toast.LENGTH_SHORT).show()
                finish()
            }
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        executor.shutdown()
    }
}
