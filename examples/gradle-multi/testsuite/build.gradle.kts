plugins {
    id("java-library")
    id("com.code-intelligence.cifuzz") version "1.0.0"
}

repositories.mavenCentral()

dependencies {
    implementation(project(":app"))
}
