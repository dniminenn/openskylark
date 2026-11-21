package openskylark.monitor;

import android.app.Activity;
import android.app.PendingIntent;
import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.graphics.Insets;
import android.graphics.Typeface;
import android.hardware.usb.UsbDevice;
import android.hardware.usb.UsbManager;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.WindowInsets;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

public final class MainActivity extends Activity {
    private static final String ACTION_PERMISSION = "openskylark.monitor.USB_PERMISSION";

    private final Handler handler = new Handler(Looper.getMainLooper());
    private UsbManager usb;
    private SharedPreferences prefs;
    private PendingIntent permissionIntent;
    private TextView text;
    private CheckBox always;
    private String notice = "";
    private boolean fake;

    private final BroadcastReceiver permission = new BroadcastReceiver() {
        @Override
        public void onReceive(Context context, Intent intent) {
            if (intent.getBooleanExtra(UsbManager.EXTRA_PERMISSION_GRANTED, false)) {
                begin();
            } else {
                notice = "Permission for the DAC was refused.";
            }
        }
    };

    private final Runnable refresh = new Runnable() {
        @Override
        public void run() {
            text.setText((notice.isEmpty() ? "" : notice + "\n\n") + MonitorService.status);
            handler.postDelayed(this, 500);
        }
    };

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        usb = (UsbManager) getSystemService(USB_SERVICE);
        prefs = getSharedPreferences("monitor", MODE_PRIVATE);
        permissionIntent = PendingIntent.getBroadcast(this, 0,
                new Intent(ACTION_PERMISSION).setPackage(getPackageName()), PendingIntent.FLAG_MUTABLE);
        IntentFilter filter = new IntentFilter(ACTION_PERMISSION);
        if (Build.VERSION.SDK_INT >= 33) {
            registerReceiver(permission, filter, RECEIVER_NOT_EXPORTED);
        } else {
            registerReceiver(permission, filter);
        }

        int pad = (int) (16 * getResources().getDisplayMetrics().density);
        LinearLayout column = new LinearLayout(this);
        column.setOrientation(LinearLayout.VERTICAL);
        column.setPadding(pad, pad, pad, pad);

        LinearLayout buttons = new LinearLayout(this);
        buttons.addView(button("Start", this::begin));
        buttons.addView(button("Stop", () -> {
            if (MonitorService.running) {
                stopService(new Intent(this, MonitorService.class));
            }
        }));
        buttons.addView(button("Mark", () -> {
            if (MonitorService.running) {
                startService(new Intent(this, MonitorService.class).setAction(MonitorService.ACTION_MARK));
            }
        }));
        buttons.addView(button("Share log", this::share));
        column.addView(buttons);

        always = new CheckBox(this);
        always.setText("Also read the DAC while nothing is playing (keeps it awake)");
        always.setChecked(prefs.getBoolean("always", false));
        always.setOnCheckedChangeListener((view, checked) -> {
            prefs.edit().putBoolean("always", checked).apply();
            if (MonitorService.running) {
                begin();
            }
        });
        column.addView(always);

        text = new TextView(this);
        text.setTypeface(Typeface.MONOSPACE);
        text.setTextIsSelectable(true);
        text.setPadding(0, pad, 0, 0);
        column.addView(text);

        ScrollView scroll = new ScrollView(this);
        scroll.addView(column);
        // Keep clear of the status and navigation bars, which the content
        // is drawn under from Android 15.
        scroll.setOnApplyWindowInsetsListener((view, insets) -> {
            Insets bars = insets.getInsets(WindowInsets.Type.systemBars() | WindowInsets.Type.displayCutout());
            view.setPadding(bars.left, bars.top, bars.right, bars.bottom);
            return insets;
        });
        setContentView(scroll);

        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission("android.permission.POST_NOTIFICATIONS") != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[] {"android.permission.POST_NOTIFICATIONS"}, 1);
        }
        fake = getIntent().getBooleanExtra(MonitorService.EXTRA_FAKE, false);
        begin();
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        // The DAC was plugged in while the app was open.
        begin();
    }

    @Override
    protected void onResume() {
        super.onResume();
        handler.post(refresh);
    }

    @Override
    protected void onPause() {
        handler.removeCallbacks(refresh);
        super.onPause();
    }

    @Override
    protected void onDestroy() {
        unregisterReceiver(permission);
        super.onDestroy();
    }

    private Button button(String label, Runnable action) {
        Button b = new Button(this);
        b.setText(label);
        b.setAllCaps(false);
        b.setOnClickListener(view -> action.run());
        return b;
    }

    private UsbDevice find() {
        for (UsbDevice d : usb.getDeviceList().values()) {
            if (d.getVendorId() == Bridge.VENDOR_ID) {
                return d;
            }
        }
        return null;
    }

    /** Starts the monitor, asking for the DAC first if that is still needed. */
    private void begin() {
        notice = "";
        if (!fake) {
            UsbDevice device = find();
            if (device == null) {
                notice = "No DAC attached (looking for USB vendor id 2fc6). Plug it in and press Start.";
                return;
            }
            if (!usb.hasPermission(device)) {
                notice = "Waiting for permission to use the DAC.";
                usb.requestPermission(device, permissionIntent);
                return;
            }
        }
        startForegroundService(new Intent(this, MonitorService.class)
                .putExtra(MonitorService.EXTRA_FAKE, fake)
                .putExtra(MonitorService.EXTRA_ALWAYS, always.isChecked()));
    }

    private void share() {
        if (MonitorService.logUri == null) {
            notice = "There is no log yet.";
            return;
        }
        Intent send = new Intent(Intent.ACTION_SEND)
                .setType("text/csv")
                .putExtra(Intent.EXTRA_STREAM, MonitorService.logUri)
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
        startActivity(Intent.createChooser(send, "Share log"));
    }
}
