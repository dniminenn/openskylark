package openskylark.monitor;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.BroadcastReceiver;
import android.content.ContentValues;
import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.content.pm.ServiceInfo;
import android.hardware.usb.UsbDevice;
import android.hardware.usb.UsbDeviceConnection;
import android.hardware.usb.UsbManager;
import android.media.AudioManager;
import android.net.Uri;
import android.os.Build;
import android.os.Handler;
import android.os.HandlerThread;
import android.os.IBinder;
import android.os.SystemClock;
import android.provider.MediaStore;

import java.io.IOException;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;

/**
 * Reads the DAC once a second and appends what it finds to a CSV file in
 * Download/openskylark.
 *
 * It takes no wake lock and, unless asked to, opens the DAC only while the
 * phone is playing something, so as to change as little as possible about
 * when the phone lets the DAC sleep.
 */
public final class MonitorService extends Service {
    static final String EXTRA_FAKE = "fake";
    static final String EXTRA_ALWAYS = "always";
    static final String ACTION_MARK = "openskylark.monitor.MARK";
    static final String ACTION_STOP = "openskylark.monitor.STOP";

    /** How long after the phone stops playing the DAC is still read. */
    private static final long LINGER_MS = 5000;

    static volatile boolean running;
    static volatile String status = "Not running";
    static volatile Uri logUri;
    static volatile String logName;

    private HandlerThread thread;
    private Handler handler;
    private UsbManager usb;
    private AudioManager audio;
    private boolean fake, always;

    private UsbDeviceConnection connection;
    private Bridge bridge;
    private OutputStream log;
    private long started, lastActive, nextTick, rows;
    private String lastEvent = "";
    private String lastNote = "";

    private final BroadcastReceiver detached = new BroadcastReceiver() {
        @Override
        public void onReceive(Context context, Intent intent) {
            handler.post(() -> {
                if (connection != null && find() == null) {
                    close("detached");
                }
            });
        }
    };

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    @Override
    public void onCreate() {
        super.onCreate();
        usb = (UsbManager) getSystemService(USB_SERVICE);
        audio = (AudioManager) getSystemService(AUDIO_SERVICE);
        thread = new HandlerThread("monitor");
        thread.start();
        handler = new Handler(thread.getLooper());
        IntentFilter filter = new IntentFilter(UsbManager.ACTION_USB_DEVICE_DETACHED);
        if (Build.VERSION.SDK_INT >= 33) {
            registerReceiver(detached, filter, RECEIVER_NOT_EXPORTED);
        } else {
            registerReceiver(detached, filter);
        }
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        startForeground(1, notification("Starting"), ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);
        String action = intent == null ? null : intent.getAction();
        if (ACTION_STOP.equals(action)) {
            stopSelf();
            return START_NOT_STICKY;
        }
        if (ACTION_MARK.equals(action)) {
            handler.post(() -> event("mark"));
            return START_NOT_STICKY;
        }
        final boolean wantFake = intent != null && intent.getBooleanExtra(EXTRA_FAKE, false);
        final boolean wantAlways = intent != null && intent.getBooleanExtra(EXTRA_ALWAYS, false);
        handler.post(() -> {
            always = wantAlways;
            if (!running) {
                fake = wantFake;
                running = true;
                started = SystemClock.elapsedRealtime();
                lastActive = started;
                nextTick = SystemClock.uptimeMillis();
                openLog();
                event(fake ? "started with a FAKE DAC" : "started");
                tick();
            } else {
                // A new start while running: the DAC was plugged in again
                // or the option changed. Look for it at once.
                lastActive = SystemClock.elapsedRealtime();
            }
        });
        return START_NOT_STICKY;
    }

    @Override
    public void onDestroy() {
        unregisterReceiver(detached);
        handler.post(() -> {
            handler.removeCallbacksAndMessages(null);
            if (running) {
                close(null);
                event("stopped");
                running = false;
            }
            try {
                if (log != null) {
                    log.close();
                }
            } catch (IOException e) {
                // Nothing left to do with it.
            }
            status = "Not running";
            thread.quitSafely();
        });
        super.onDestroy();
    }

    private void tick() {
        long now = SystemClock.elapsedRealtime();
        boolean music = audio.isMusicActive();
        if (music) {
            lastActive = now;
        }
        String note;
        Sample sample = null;
        if (!fake && !always && now - lastActive >= LINGER_MS) {
            if (connection != null) {
                close("nothing playing, DAC left alone");
            }
            note = "Nothing is playing: not reading the DAC";
        } else {
            note = open();
            if (bridge != null) {
                try {
                    sample = bridge.sample();
                } catch (IOException e) {
                    close("error: " + e.getMessage());
                    note = "Reading the DAC failed: " + e.getMessage();
                }
            }
        }
        if (sample != null) {
            row("", music, sample.csv());
            note = (fake ? "FAKE DAC (test mode)\n" : "") + sample.summary();
        } else if (!note.equals(lastNote)) {
            event(note);
        }
        lastNote = sample != null ? "" : note;
        publish(note);
        nextTick += 1000;
        long uptime = SystemClock.uptimeMillis();
        if (nextTick < uptime) {
            nextTick = uptime + 1000; // the phone slept, or a read took long
        }
        handler.postAtTime(this::tick, nextTick);
    }

    private UsbDevice find() {
        for (UsbDevice d : usb.getDeviceList().values()) {
            if (d.getVendorId() == Bridge.VENDOR_ID) {
                return d;
            }
        }
        return null;
    }

    /** Opens the DAC if it is not open. Returns what to tell the user when it cannot. */
    private String open() {
        if (bridge != null) {
            return "";
        }
        if (fake) {
            bridge = new Bridge(new FakeChip());
            return "";
        }
        UsbDevice device = find();
        if (device == null) {
            return "No DAC attached";
        }
        if (!usb.hasPermission(device)) {
            return "No permission for the DAC: open the app and allow it";
        }
        // No interface is claimed: the phone's audio driver keeps the DAC.
        final UsbDeviceConnection c = usb.openDevice(device);
        if (c == null) {
            return "The DAC could not be opened";
        }
        connection = c;
        bridge = new Bridge((type, request, value, index, data, length) -> {
            int n = c.controlTransfer(type, request, value, index, data, length, 500);
            if (n < 0) {
                throw new IOException("control transfer failed (" + n + ")");
            }
            return n;
        });
        event("opened " + device.getProductName() + " " + String.format("%04x:%04x", device.getVendorId(), device.getProductId()));
        return "";
    }

    private void close(String why) {
        if (connection != null) {
            connection.close();
            connection = null;
        }
        bridge = null;
        if (why != null) {
            event(why);
        }
    }

    private void openLog() {
        String name = "openskylark-" + new SimpleDateFormat("yyyyMMdd-HHmmss", Locale.US).format(new Date()) + ".csv";
        ContentValues values = new ContentValues();
        values.put(MediaStore.Downloads.DISPLAY_NAME, name);
        values.put(MediaStore.Downloads.MIME_TYPE, "text/csv");
        values.put(MediaStore.Downloads.RELATIVE_PATH, "Download/openskylark");
        try {
            Uri uri = getContentResolver().insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values);
            log = getContentResolver().openOutputStream(uri, "wa");
            log.write(("time,seconds,event,music_active," + Sample.CSV_HEADER + "\n").getBytes(StandardCharsets.UTF_8));
            log.flush();
            logUri = uri;
            logName = name;
        } catch (Exception e) {
            log = null;
            logUri = null;
            logName = "no log: " + e;
        }
    }

    private void event(String what) {
        lastEvent = what;
        row(what, audio.isMusicActive(), "");
    }

    private void row(String event, boolean music, String rest) {
        if (log == null) {
            return;
        }
        long seconds = (SystemClock.elapsedRealtime() - started) / 1000;
        String time = new SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US).format(new Date());
        String line = time + "," + seconds + ",\"" + event.replace('"', '\'').replace('\n', ' ') + "\"," + (music ? 1 : 0) + "," + rest + "\n";
        try {
            log.write(line.getBytes(StandardCharsets.UTF_8));
            log.flush();
            rows++;
        } catch (IOException e) {
            logName = "log write failed: " + e.getMessage();
        }
    }

    private void publish(String note) {
        String text = note + "\n\nLog: " + logName + "\n" + rows + " rows" + (lastEvent.isEmpty() ? "" : "\nLast event: " + lastEvent);
        if (!text.equals(status)) {
            status = text;
        }
        String line = note.split("\n")[0];
        getSystemService(NotificationManager.class).notify(1, notification(line));
    }

    private Notification notification(String text) {
        NotificationManager manager = getSystemService(NotificationManager.class);
        if (manager.getNotificationChannel("monitor") == null) {
            manager.createNotificationChannel(new NotificationChannel("monitor", "Monitor", NotificationManager.IMPORTANCE_LOW));
        }
        PendingIntent show = PendingIntent.getActivity(this, 0, new Intent(this, MainActivity.class), PendingIntent.FLAG_IMMUTABLE);
        return new Notification.Builder(this, "monitor")
                .setSmallIcon(R.drawable.icon)
                .setContentTitle("openskylark monitor")
                .setContentText(text)
                .setContentIntent(show)
                .setOnlyAlertOnce(true)
                .setOngoing(true)
                .build();
    }
}
