package cn.dsh.yanhekt;

import android.app.Activity;
import android.content.Intent;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.util.Log;
import android.view.Gravity;
import android.view.ViewGroup;
import android.view.Window;
import android.webkit.CookieManager;
import android.webkit.DownloadListener;
import android.webkit.JavascriptInterface;
import android.webkit.WebChromeClient;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.TextView;
import android.widget.Toast;

import java.io.BufferedReader;
import java.io.File;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;

/**
 * 薄壳：把 Go 版引擎（打包成 lib/arm64-v8a/libyhkt.so）拉起来，WebView 开 127.0.0.1。
 *
 * 和电脑版唯一的差别在登录：电脑上是从浏览器 profile 里捞 token / 点一次书签，
 * 手机上让用户在 WebView 里正常登录，然后我们把 cookie（老平台 yht、新版 _token）
 * 直接交给本地引擎 —— 效果和电脑版一致。
 */
public class MainActivity extends Activity {

    private static final String TAG = "yhkt";
    private static final int PORT = 8799;
    private static final String BASE = "http://127.0.0.1:" + PORT;
    private static final String OLD_SITE = "https://www.yanhekt.cn";
    private static final String Y2_SITE = "https://aita.yanhekt.cn";

    private WebView web;
    private TextView bar;
    private Process proc;
    private final Handler ui = new Handler(Looper.getMainLooper());
    private volatile boolean loggingIn = false;
    private String pendingUrl = null;

    @Override
    protected void onCreate(Bundle b) {
        super.onCreate(b);
        requestWindowFeature(Window.FEATURE_NO_TITLE);
        CookieManager cm = CookieManager.getInstance();
        cm.setAcceptCookie(true);

        FrameLayout root = new FrameLayout(this);
        root.setBackgroundColor(Color.parseColor("#0f1115"));

        web = new WebView(this);
        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setDatabaseEnabled(true);
        s.setMediaPlaybackRequiresUserGesture(false);
        s.setLoadWithOverviewMode(true);
        s.setUseWideViewPort(true);
        s.setMixedContentMode(WebSettings.MIXED_CONTENT_ALWAYS_ALLOW);
        s.setUserAgentString(s.getUserAgentString());
        s.setSupportMultipleWindows(false);
        cm.setAcceptThirdPartyCookies(web, true);

        web.addJavascriptInterface(this, "yhktApp");
        web.setWebViewClient(new WebViewClient() {
            @Override public void onPageFinished(WebView v, String url) {
                v.evaluateJavascript(DL_HOOK, null);
            }

            @Override public boolean shouldOverrideUrlLoading(WebView v, String url) {
                if (url == null) return false;
                boolean ours = url.startsWith(BASE) || url.contains("yanhekt.cn");
                if (ours) return false;
                try { startActivity(new Intent(Intent.ACTION_VIEW, Uri.parse(url))); } catch (Exception ignored) {}
                return true;
            }
        });
        web.setWebChromeClient(new WebChromeClient());
        web.setDownloadListener(new DownloadListener() {
            @Override public void onDownloadStart(String url, String userAgent, String contentDisposition, String mimeType, long len) {
                download(url);
            }
        });

        root.addView(web, new FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));

        bar = new TextView(this);
        bar.setTextColor(Color.parseColor("#9aa3b2"));
        bar.setTextSize(13f);
        bar.setGravity(Gravity.CENTER_VERTICAL);
        bar.setPadding(dp(14), dp(10), dp(14), dp(10));
        bar.setBackgroundColor(Color.parseColor("#2a1f1f"));
        bar.setText("正在启动本地引擎…");
        FrameLayout.LayoutParams lp = new FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.gravity = Gravity.BOTTOM;
        root.addView(bar, lp);

        setContentView(root);
        startEngine();
    }

    private int dp(int v) { return (int) (v * getResources().getDisplayMetrics().density); }

    // ---------------------------------------------------------------- 引擎

    private void startEngine() {
        new Thread(new Runnable() { public void run() {
            File bin = new File(getApplicationInfo().nativeLibraryDir, "libyhkt.so");
            Log.i(TAG, "engine " + bin.getAbsolutePath() + " exists=" + bin.exists());
            if (!bin.exists()) { say("找不到内置引擎，安装包可能被改动"); return; }
            try {
                ProcessBuilder pb = new ProcessBuilder(bin.getAbsolutePath());
                pb.redirectErrorStream(true);
                pb.environment().put("PORT", String.valueOf(PORT));
                pb.environment().put("FORCE_NEW", "1");
                pb.environment().put("HOME", getFilesDir().getAbsolutePath());
                pb.environment().put("TMPDIR", getCacheDir().getAbsolutePath());
                proc = pb.start();
                final BufferedReader r = new BufferedReader(new InputStreamReader(proc.getInputStream()));
                new Thread(new Runnable() { public void run() {
                    try { String line; while ((line = r.readLine()) != null) Log.i(TAG, "engine> " + line); } catch (Exception ignored) {}
                }}).start();
            } catch (Exception e) {
                Log.e(TAG, "engine failed", e);
                say("本地引擎启动失败：" + e.getMessage());
                return;
            }
            if (!waitUp(20000)) { say("本地引擎没起来（20 秒超时）"); return; }
            ui.post(new Runnable() { public void run() { afterEngine(); } });
        }}).start();
    }

    private boolean waitUp(int ms) {
        long t0 = System.currentTimeMillis();
        while (System.currentTimeMillis() - t0 < ms) {
            String s = tryHttp(BASE + "/api/status", null);
            if (s != null && s.indexOf("loggedIn") >= 0) return true;
            sleep(400);
        }
        return false;
    }

    private void afterEngine() {
        String oldSt = tryHttp(BASE + "/api/status", null);
        String newSt = tryHttp(BASE + "/api/y2/status", null);
        boolean oldOk = oldSt != null && oldSt.indexOf("\"loggedIn\":true") >= 0;
        boolean newOk = newSt != null && newSt.indexOf("\"loggedIn\":true") >= 0;
        Log.i(TAG, "loggedIn old=" + oldOk + " y2=" + newOk);
        if (oldOk) { openPlayer(); return; }
        startLogin();
    }

    // ---------------------------------------------------------------- 登录（两步：老平台 → 新版平台）

    private void startLogin() {
        if (loggingIn) return;
        loggingIn = true;
        bar.setVisibility(TextView.VISIBLE);
        bar.setText("请在下方登录延河课堂；老平台、新版平台各登录一次，之后会自动进入播放器");
        web.loadUrl(OLD_SITE);
        new Thread(new Runnable() { public void run() {
            // 1) 老平台：cookie yht（或 localStorage.auth.token）
            String tok = waitOldToken(120000);
            if (tok != null) {
                String r = postLogin("/api/login", "{\"token\":" + json(tok) + "}");
                Log.i(TAG, "old login -> " + r);
                ui.post(new Runnable() { public void run() {
                    bar.setText("老平台已登录。接下来登录新版平台（浏览器里同一个账号再登一次）");
                    web.loadUrl(Y2_SITE + "/portal/");
                }});
            }
            // 2) 新版平台：cookie _token
            String ck = waitY2Cookie(150000);
            if (ck != null) {
                String r2 = postLogin("/api/y2/login", "{\"cookie\":" + json(ck) + "}");
                Log.i(TAG, "y2 login -> " + r2);
            }
            loggingIn = false;
            ui.post(new Runnable() { public void run() {
                if (tok == null && ck == null) {
                    bar.setText("没检测到登录。登录完成后点这里重试");
                    bar.setOnClickListener(new android.view.View.OnClickListener() {
                        @Override public void onClick(android.view.View v) { startLogin(); }
                    });
                    return;
                }
                openPlayer();
            }});
        }}).start();
    }

    private String waitOldToken(int ms) {
        long t0 = System.currentTimeMillis();
        while (System.currentTimeMillis() - t0 < ms) {
            String ck = CookieManager.getInstance().getCookie(OLD_SITE);
            String tok = cookieValue(ck, "yht");
            if (tok != null && tok.length() > 20) return Uri.decode(tok);
            sleep(1200);
        }
        return null;
    }

    private String waitY2Cookie(int ms) {
        long t0 = System.currentTimeMillis();
        while (System.currentTimeMillis() - t0 < ms) {
            String ck = CookieManager.getInstance().getCookie(Y2_SITE);
            if (ck != null && ck.indexOf("_token") >= 0) return ck;
            sleep(1200);
        }
        return null;
    }

    private static String cookieValue(String header, String name) {
        if (header == null) return null;
        for (String kv : header.split(";")) {
            int i = kv.indexOf('=');
            if (i <= 0) continue;
            if (kv.substring(0, i).trim().equals(name)) return kv.substring(i + 1).trim();
        }
        return null;
    }

    private void openPlayer() {
        loggingIn = false;
        ui.post(new Runnable() { public void run() {
            bar.setVisibility(TextView.GONE);
            web.loadUrl(BASE + "/");
            ui.removeCallbacks(watch);
            ui.postDelayed(watch, 60000);
        }});
    }

    /** 登录态约 24 小时；过期后页面上的书签登录在手机上没用，这里给一个"点一下重登"的入口 */
    private final Runnable watch = new Runnable() { public void run() {
        if (loggingIn) return;
        new Thread(new Runnable() { public void run() {
            String st = tryHttp(BASE + "/api/y2/status", null);
            final boolean ok = st != null && st.indexOf("\"loggedIn\":true") >= 0;
            ui.post(new Runnable() { public void run() {
                if (loggingIn) return;
                if (ok) { ui.postDelayed(watch, 60000); return; }
                bar.setVisibility(TextView.VISIBLE);
                bar.setText("登录已过期 —— 点这里重新登录");
                bar.setOnClickListener(new android.view.View.OnClickListener() {
                    @Override public void onClick(android.view.View v) {
                        bar.setOnClickListener(null);
                        startLogin();
                    }
                });
            }});
        }}).start();
    }};

    // ---------------------------------------------------------------- 工具

    private void say(final String msg) {
        Log.w(TAG, msg);
        ui.post(new Runnable() { public void run() { bar.setVisibility(TextView.VISIBLE); bar.setText(msg); } });
    }

    private static void sleep(long ms) { try { Thread.sleep(ms); } catch (InterruptedException ignored) {} }

    private static String json(String s) {
        StringBuilder b = new StringBuilder("\"");
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            if (c == '"') b.append("\\\"");
            else if (c == '\\') b.append("\\\\");
            else if (c == '\n') b.append("\\n");
            else if (c == '\r') b.append("\\r");
            else if (c == '\t') b.append("\\t");
            else if (c < 0x20) b.append(String.format("\\u%04x", (int) c));
            else b.append(c);
        }
        return b.append('"').toString();
    }

    private static String tryHttp(String url, String body) {
        try { return http(url, body); } catch (Exception e) { return null; }
    }

    /**
     * 登录必须走这里：引擎会用 Set-Cookie 下发会话，但原生请求种下的 cookie WebView 看不到，
     * 所以要把响应的 Set-Cookie 抄进 CookieManager，否则页面还是"未登录"。
     */
    private String postLogin(String path, String body) {
        HttpURLConnection c = null;
        try {
            c = (HttpURLConnection) new URL(BASE + path).openConnection();
            c.setConnectTimeout(5000);
            c.setReadTimeout(40000);
            c.setRequestMethod("POST");
            c.setDoOutput(true);
            c.setRequestProperty("content-type", "application/json");
            OutputStream os = c.getOutputStream();
            os.write(body.getBytes("UTF-8"));
            os.close();
            for (java.util.Map.Entry<String, java.util.List<String>> e : c.getHeaderFields().entrySet()) {
                if (e.getKey() == null || !e.getKey().equalsIgnoreCase("Set-Cookie")) continue;
                for (String sc : e.getValue()) {
                    String pair = sc.split(";", 2)[0];
                    CookieManager.getInstance().setCookie(BASE, pair);
                    Log.i(TAG, "cookie <- " + pair.split("=", 2)[0]);
                }
            }
            CookieManager.getInstance().flush();
            java.io.InputStream in = c.getResponseCode() >= 400 ? c.getErrorStream() : c.getInputStream();
            if (in == null) return null;
            BufferedReader r = new BufferedReader(new InputStreamReader(in, "UTF-8"));
            StringBuilder sb = new StringBuilder();
            String line;
            while ((line = r.readLine()) != null) sb.append(line);
            r.close();
            return sb.toString();
        } catch (Exception e) {
            Log.e(TAG, "postLogin failed: " + path, e);
            return null;
        } finally { if (c != null) c.disconnect(); }
    }

    private static String http(String url, String body) throws Exception {
        HttpURLConnection c = (HttpURLConnection) new URL(url).openConnection();
        c.setConnectTimeout(4000);
        c.setReadTimeout(40000);
        if (body != null) {
            c.setRequestMethod("POST");
            c.setDoOutput(true);
            c.setRequestProperty("content-type", "application/json");
            OutputStream os = c.getOutputStream();
            os.write(body.getBytes("UTF-8"));
            os.close();
        }
        int code = c.getResponseCode();
        java.io.InputStream in = code >= 400 ? c.getErrorStream() : c.getInputStream();
        if (in == null) return null;
        BufferedReader r = new BufferedReader(new InputStreamReader(in, "UTF-8"));
        StringBuilder sb = new StringBuilder();
        String line;
        while ((line = r.readLine()) != null) sb.append(line);
        r.close();
        return sb.toString();
    }

    // ---------------------------------------------------------------- 下载

    /**
     * 页面里点「下载」会 window.open('/y2dl?...')；WebView 自己处理不了这种流，
     * 这里把请求接过来：带上网关 cookie 直接拉，按响应里的文件名存进系统下载目录。
     */
    @JavascriptInterface
    public void download(final String url) {
        Log.i(TAG, "download " + url);
        if (Build.VERSION.SDK_INT < 29 && checkSelfPermission(android.Manifest.permission.WRITE_EXTERNAL_STORAGE) != 0) {
            pendingUrl = url;
            requestPermissions(new String[]{android.Manifest.permission.WRITE_EXTERNAL_STORAGE}, 41);
            return;
        }
        startDownload(url);
    }

    @Override public void onRequestPermissionsResult(int req, String[] perms, int[] granted) {
        super.onRequestPermissionsResult(req, perms, granted);
        if (req == 41 && pendingUrl != null) {
            String u = pendingUrl; pendingUrl = null;
            if (granted.length > 0 && granted[0] == 0) startDownload(u);
            else say("没有存储权限，下载不了");
        }
    }

    private void startDownload(final String url) {
        Toast.makeText(this, "开始下载，别关这个页面", Toast.LENGTH_LONG).show();
        new Thread(new Runnable() { public void run() {
            HttpURLConnection c = null;
            Uri target = null;
            try {
                c = (HttpURLConnection) new URL(url).openConnection();
                String cookie = CookieManager.getInstance().getCookie(BASE);
                if (cookie != null) c.setRequestProperty("Cookie", cookie);
                c.setConnectTimeout(6000);
                c.setReadTimeout(60000);
                int code = c.getResponseCode();
                if (code >= 400) throw new java.io.IOException("HTTP " + code);
                String name = fileName(c.getHeaderField("content-disposition"));
                if (name == null) name = "lecture_" + System.currentTimeMillis() + ".mp4";
                String mime = name.endsWith(".ts") ? "video/mp2t" : "video/mp4";
                OutputStream os;
                if (Build.VERSION.SDK_INT >= 29) {
                    android.content.ContentValues cv = new android.content.ContentValues();
                    cv.put(android.provider.MediaStore.MediaColumns.DISPLAY_NAME, name);
                    cv.put(android.provider.MediaStore.MediaColumns.MIME_TYPE, mime);
                    cv.put(android.provider.MediaStore.MediaColumns.RELATIVE_PATH, android.os.Environment.DIRECTORY_DOWNLOADS);
                    cv.put(android.provider.MediaStore.MediaColumns.IS_PENDING, 1);
                    target = getContentResolver().insert(android.provider.MediaStore.Downloads.EXTERNAL_CONTENT_URI, cv);
                    if (target == null) throw new java.io.IOException("建不了下载文件");
                    os = getContentResolver().openOutputStream(target);
                } else {
                    File dir = android.os.Environment.getExternalStoragePublicDirectory(android.os.Environment.DIRECTORY_DOWNLOADS);
                    if (!dir.exists()) dir.mkdirs();
                    os = new java.io.FileOutputStream(new File(dir, name));
                }
                if (os == null) throw new java.io.IOException("写不了文件");
                java.io.InputStream in = c.getInputStream();
                byte[] buf = new byte[64 * 1024];
                long total = 0;
                int n;
                while ((n = in.read(buf)) > 0) { os.write(buf, 0, n); total += n; }
                os.flush(); os.close(); in.close();
                if (Build.VERSION.SDK_INT >= 29 && target != null) {
                    android.content.ContentValues done = new android.content.ContentValues();
                    done.put(android.provider.MediaStore.MediaColumns.IS_PENDING, 0);
                    getContentResolver().update(target, done, null, null);
                }
                final String msg = "下载完成：" + name + "（" + (total / 1048576.0) + " MB，在系统「下载」里）";
                Log.i(TAG, msg);
                ui.post(new Runnable() { public void run() { Toast.makeText(MainActivity.this, msg, Toast.LENGTH_LONG).show(); } });
            } catch (Exception e) {
                Log.e(TAG, "download failed", e);
                say("下载失败：" + e.getMessage());
            } finally { if (c != null) c.disconnect(); }
        }}).start();
    }

    private static String fileName(String cd) {
        if (cd == null) return null;
        java.util.regex.Matcher m = java.util.regex.Pattern.compile("filename\\*=UTF-8''([^;]+)").matcher(cd);
        if (m.find()) { try { return java.net.URLDecoder.decode(m.group(1), "UTF-8"); } catch (Exception ignored) {} }
        m = java.util.regex.Pattern.compile("filename=\"?([^\";]+)\"?").matcher(cd);
        if (m.find()) return m.group(1);
        return null;
    }

    // 页面里 window.open('/y2dl…') 劫持到原生下载；其它 window.open 原样放行
    private static final String DL_HOOK =
        "(function(){if(window.__yhktDlHook)return;window.__yhktDlHook=1;"
        + "var o=window.open;window.open=function(u,n,f){"
        + "try{if(u&&/^(\\/y2dl|\\/dl)/.test(u)){yhktApp.download(new URL(u,location.href).href);return null;}}catch(e){}"
        + "try{return o.call(window,u,n,f);}catch(e){return null;}};})();";

    // ---------------------------------------------------------------- 生命周期

    @Override public void onBackPressed() {
        if (web != null && web.canGoBack()) { web.goBack(); return; }
        super.onBackPressed();
    }

    @Override protected void onDestroy() {
        try { if (proc != null) proc.destroy(); } catch (Exception ignored) {}
        if (web != null) { web.loadUrl("about:blank"); web.destroy(); }
        super.onDestroy();
    }
}
