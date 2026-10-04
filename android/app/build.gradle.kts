plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "dev.pulse.app"
    compileSdk = 34

    defaultConfig {
        applicationId = "dev.pulse.app"
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "0.1"
    }

    buildTypes {
        // Demo app: only the debug build (debug-signed) is used.
        release {
            isMinifyEnabled = false
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures {
        buildConfig = true
    }
}

dependencies {
    // The native WebSocket used while the screen is off.
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
}
