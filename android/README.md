# SadakWatch for Android

Turns a phone mounted on the dashboard into a SadakWatch sensor.

**Download:** [`sadakwatch.apk`](https://github.com/arvinderss/sadakwatch/releases/download/android-latest/sadakwatch.apk) (Android 8.0+). Every push to `main` rebuilds it; tags `v*` produce versioned releases.

## What it does

* **Automatic bump detection.** The accelerometer's vertical component (relative to gravity, so any mount angle works) is watched for impacts. A drop-then-impact is reported as a `pothole`, a lift-first jolt as a `speed_breaker`; the size of the spike sets the severity. Bumps below the configured speed (default 12 km/h) are ignored.
* **Photo evidence.** While driving, the camera keeps the last ~2.5 s of frames. A report attaches the frame from just before the impact, when the defect was still visible ahead.
* **One-tap reports.** Pothole, Waterlogging, or *Other…* for the full taxonomy.
* **Works offline.** Reports are written to an on-device queue first and uploaded by WorkManager whenever there is a network, with retries. Event IDs are generated on the phone, so re-uploads never create duplicates.

Recording runs while the app is on screen (it keeps the screen on during a drive). Background recording is not implemented yet.

## Setup

Settings → API URL (e.g. `http://192.168.1.20:8080`), optional ingest key, vehicle ID, sensitivity (Low for stiff suspensions such as autos and buses, High for soft cars), minimum speed, and whether to attach photos. Use **Test connection** before driving.

The **Map** button opens the server's dashboard.

## Building

```bash
./gradlew testDebugUnitTest assembleDebug     # app/build/outputs/apk/debug/
./gradlew assembleRelease                     # app/build/outputs/apk/release/
```

Requires JDK 17 and the Android SDK (API 35). Release signing reads `SIGNING_KEYSTORE_PATH`, `SIGNING_STORE_PASSWORD`, `SIGNING_KEY_ALIAS`, `SIGNING_KEY_PASSWORD`; without them the release build is signed with the committed debug key (`app/debug.keystore`), which is fine for testing but must be replaced before public distribution. In CI, set the repository secrets `ANDROID_KEYSTORE_BASE64`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`:

```bash
keytool -genkeypair -v -keystore release.keystore -alias sadakwatch -keyalg RSA -keysize 4096 -validity 10000
base64 -w0 release.keystore   # value for ANDROID_KEYSTORE_BASE64
```

Switching from the debug key to a release key changes the signature, so testers need to uninstall the old build once.
