#!/bin/sh
# Builds build/openskylark-monitor.apk with the Android SDK's own tools: no
# Gradle. Needs build-tools and the android-35 platform under $ANDROID_HOME,
# and a JDK.
#
#   ./build.sh          test, then build and sign with a debug key
#   ./build.sh test     only run the protocol tests (needs only the JDK)
set -eu
cd "$(dirname "$0")"

SDK=${ANDROID_HOME:-$HOME/Android/Sdk}
KEYSTORE=${KEYSTORE:-$HOME/.android/debug.keystore}
MIN=30
TARGET=35

rm -rf build
mkdir -p build/classes-test build/classes build/gen build/dex

# The protocol code has no Android in it and is tested on the JDK.
javac -Xlint:all -Werror -d build/classes-test \
	src/openskylark/monitor/Bridge.java src/openskylark/monitor/Sample.java \
	src/openskylark/monitor/FakeChip.java test/BridgeTest.java
java -cp build/classes-test openskylark.monitor.BridgeTest
[ "${1:-}" = test ] && exit 0

TOOLS=$(ls -d "$SDK"/build-tools/* | sort -V | tail -1)
JAR=$SDK/platforms/android-$TARGET/android.jar
[ -f "$JAR" ] || { echo "no $JAR" >&2; exit 1; }

"$TOOLS/aapt2" compile --dir res -o build/res.zip
"$TOOLS/aapt2" link -o build/unsigned.apk -I "$JAR" --manifest AndroidManifest.xml \
	--min-sdk-version $MIN --target-sdk-version $TARGET --version-code 1 --version-name 1 \
	--java build/gen build/res.zip
javac -Xlint:all -Xlint:-options -source 17 -target 17 -classpath "$JAR" -d build/classes \
	src/openskylark/monitor/*.java build/gen/openskylark/monitor/R.java
"$TOOLS/d8" --release --lib "$JAR" --min-api $MIN --output build/dex $(find build/classes -name '*.class')
jar --update --file build/unsigned.apk -C build/dex classes.dex
"$TOOLS/zipalign" -f 4 build/unsigned.apk build/aligned.apk

if [ ! -f "$KEYSTORE" ]; then
	mkdir -p "$(dirname "$KEYSTORE")"
	keytool -genkeypair -keystore "$KEYSTORE" -storepass android -keypass android -alias androiddebugkey \
		-dname "CN=Android Debug,O=Android,C=US" -keyalg RSA -keysize 2048 -validity 10000
fi
"$TOOLS/apksigner" sign --ks "$KEYSTORE" --ks-pass pass:android --key-pass pass:android \
	--out build/openskylark-monitor.apk build/aligned.apk
"$TOOLS/apksigner" verify build/openskylark-monitor.apk
echo "build/openskylark-monitor.apk"
