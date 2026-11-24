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
    private volatile String pendingLogin = null;   // 正在跳官网登录的平台（"old"/"y2"）
    private volatile boolean loginSiteSeen = false; // 是否已经站到官网登录页上了
    private String pendingUrl = null;
    private String pendingBatchJson = null;
    private final java.util.List<String> sessionCookies = new java.util.ArrayList<String>();
    private FrameLayout rootView;
    private android.widget.LinearLayout dlPanel;
    private android.widget.LinearLayout dlList;
    private android.widget.ScrollView dlScroll;
    private TextView dlTitleView;
    private TextView dlTipView;
    private android.widget.Button dlOpenBtn;
    private android.widget.Button dlClearDoneBtn;
    private android.widget.Button cleanBtn;
    private android.widget.LinearLayout loginBar;
    private android.widget.Button loginBackBtn;
    private TextView loginTipView;
    private volatile boolean loginRedirecting = false;
    private volatile int ownedCount = -1;
    private volatile boolean darkTheme = false;


    private volatile String lastReqDump = "";
    private volatile String capturedOld = null;
    private volatile String capturedY2 = null;
    private android.view.View customView;
    private WebChromeClient.CustomViewCallback customCb;
    private volatile boolean forceNext = false;
    private volatile boolean watchOldLogged = false;
    private volatile boolean watchY2Logged = false;
    private volatile String lastLoginTarget = "y2";
    private volatile String lastPlayerUrl = null;
    private long lastBackPressMs = 0L;

    @Override
    protected void onCreate(Bundle b) {
        super.onCreate(b);
        requestWindowFeature(Window.FEATURE_NO_TITLE);
        CookieManager cm = CookieManager.getInstance();
        cm.setAcceptCookie(true);

        darkTheme = (getResources().getConfiguration().uiMode
            & android.content.res.Configuration.UI_MODE_NIGHT_MASK)
            == android.content.res.Configuration.UI_MODE_NIGHT_YES;

        FrameLayout root = new FrameLayout(this);
        rootView = root;
        root.setBackgroundColor(Color.parseColor(darkTheme ? "#131314" : "#f8fafd"));

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
                boolean onPlayer = url != null && url.startsWith(BASE);
                if (onPlayer) {
                    // 登录状态推给页面（顶栏的登录按钮据此显示「✓ 已登录」）
                    pushStatus();
                    if (loginBar != null) loginBar.setVisibility(android.view.View.GONE);
                    loginRedirecting = false;
                    // 用户从登录页按返回键回来了：把提示收掉
                    if (loginSiteSeen && pendingLogin != null) {
                        loginSiteSeen = false;
                        pendingLogin = null;
                        bar.setOnClickListener(null);
                        bar.setVisibility(TextView.GONE);
                    }
                } else {
                    loginSiteSeen = true;   // 已经真的站到官网登录页上了
                    if (pendingLogin != null && loginBar != null) {
                        loginBar.setVisibility(android.view.View.VISIBLE);
                    }
                }
            }

            // 从页面自己发出的 API 请求里截 Authorization —— 比在 localStorage 里猜可靠得多
            @Override public android.webkit.WebResourceResponse shouldInterceptRequest(
                    WebView v, android.webkit.WebResourceRequest req) {
                try {
                    String host = req.getUrl() == null ? null : req.getUrl().getHost();
                    if (host == null || !host.endsWith("yanhekt.cn")) return null;
                    StringBuilder names = new StringBuilder();
                    for (String hk : req.getRequestHeaders().keySet()) names.append(hk).append(',');
                    String u = req.getUrl().toString();
                    int q = u.indexOf('?');
                    if (u.contains("/api/") || u.contains("/v1/") || u.contains("/courseapi/")) {
                        lastReqDump = (q > 0 ? u.substring(0, Math.min(q, u.length())) : u) + "  headers=[" + names + "]";
                    }
                    for (java.util.Map.Entry<String, String> e : req.getRequestHeaders().entrySet()) {
                        String k = e.getKey() == null ? "" : e.getKey().toLowerCase();
                        String val = e.getValue();
                        if (val == null || val.length() < 20) continue;
                        if (!(k.contains("authorization") || k.contains("token"))) continue;
                        String t = val.replace("Bearer ", "").replace("bearer ", "").trim();
                        if (t.length() < 20) continue;
                        if (host.contains("aita")) { if (capturedY2 == null) capturedY2 = t; }
                        else { capturedOld = t; }
                        Log.i(TAG, "captured token from " + host + " via " + k);
                    }
                } catch (Exception ignored) {}
                return null;
            }
            @Override public boolean shouldOverrideUrlLoading(WebView v, String url) {
                if (url == null) return false;
                boolean ours = url.startsWith(BASE) || url.contains("yanhekt.cn");
                if (ours) return false;
                try { startActivity(new Intent(Intent.ACTION_VIEW, Uri.parse(url))); } catch (Exception ignored) {}
                return true;
            }
        });
        // HTML5 全屏：WebView 自己不会处理 requestFullscreen，必须接这两个回调
        web.setWebChromeClient(new WebChromeClient() {
            @Override public void onShowCustomView(android.view.View view, WebChromeClient.CustomViewCallback cb) {
                if (customView != null) { cb.onCustomViewHidden(); return; }
                customView = view; customCb = cb;
                FrameLayout.LayoutParams lp = new FrameLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT);
                rootView.addView(view, lp);
                web.setVisibility(android.view.View.GONE);
                getWindow().addFlags(android.view.WindowManager.LayoutParams.FLAG_FULLSCREEN);
                setRequestedOrientation(android.content.pm.ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE);
                hideSystemBars();
            }
            @Override public void onHideCustomView() { exitFullscreen(); }
            @Override public boolean onConsoleMessage(android.webkit.ConsoleMessage m) {
                Log.d(TAG, "js> " + m.message());
                return true;
            }
        });
        web.setDownloadListener(new DownloadListener() {
            @Override public void onDownloadStart(String url, String userAgent, String contentDisposition, String mimeType, long len) {
                download(url);
            }
        });

        android.widget.LinearLayout webWrap = new android.widget.LinearLayout(this);
        webWrap.setOrientation(android.widget.LinearLayout.VERTICAL);

        loginBar = new android.widget.LinearLayout(this);
        loginBar.setOrientation(android.widget.LinearLayout.HORIZONTAL);
        loginBar.setGravity(Gravity.CENTER_VERTICAL);
        loginBar.setPadding(dp(12), dp(8), dp(14), dp(8));
        loginBar.setVisibility(android.view.View.GONE);
        loginBackBtn = mkBtn("← 返回播放器", 2, new android.view.View.OnClickListener() {
            @Override public void onClick(android.view.View v) { backToPlayer(); }
        });
        loginBar.addView(loginBackBtn);
        loginTipView = new TextView(this);
        loginTipView.setText("请登录，成功后自动返回");
        loginTipView.setTextSize(12.5f);
        loginTipView.setPadding(dp(12), 0, 0, 0);
        loginTipView.setSingleLine(true);
        loginTipView.setEllipsize(android.text.TextUtils.TruncateAt.END);
        loginBar.addView(loginTipView, new android.widget.LinearLayout.LayoutParams(
            0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        webWrap.addView(loginBar, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        webWrap.addView(web, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f));
        root.addView(webWrap, new FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));

        bar = new TextView(this);
        bar.setTextSize(13f);
        bar.setGravity(Gravity.CENTER_VERTICAL);
        bar.setPadding(dp(16), dp(11), dp(16), dp(11));
        bar.setText("正在启动本地引擎…");
        // 长按底栏看诊断（接口原文 / 拦截到的 token / cookie / 引擎 status），平时不占地方
        bar.setOnLongClickListener(new android.view.View.OnLongClickListener() {
            @Override public boolean onLongClick(android.view.View v) {
                showDiag("手动触发");
                return true;
            }
        });
        FrameLayout.LayoutParams lp = new FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.gravity = Gravity.BOTTOM;
        root.addView(bar, lp);

        // 下载面板（Material Design 3）：列出任务、排队状态、实时速度、重试/取消/清理（留出底部 64dp 导航条）
        dlPanel = new android.widget.LinearLayout(this);
        dlPanel.setOrientation(android.widget.LinearLayout.VERTICAL);
        dlPanel.setPadding(dp(18), dp(20), dp(18), dp(12));
        dlPanel.setVisibility(android.view.View.GONE);
        dlPanel.setClickable(true);
        if (Build.VERSION.SDK_INT >= 23) {
            loginBar.setOnApplyWindowInsetsListener(new android.view.View.OnApplyWindowInsetsListener() {
                @Override public android.view.WindowInsets onApplyWindowInsets(android.view.View v, android.view.WindowInsets ins) {
                    v.setPadding(
                        dp(12) + ins.getSystemWindowInsetLeft(),
                        dp(8) + ins.getSystemWindowInsetTop(),
                        dp(14) + ins.getSystemWindowInsetRight(),
                        dp(8));
                    return ins;
                }
            });
            dlPanel.setOnApplyWindowInsetsListener(new android.view.View.OnApplyWindowInsetsListener() {
                @Override public android.view.WindowInsets onApplyWindowInsets(android.view.View v, android.view.WindowInsets ins) {
                    ViewGroup.LayoutParams rawLp = v.getLayoutParams();
                    if (rawLp instanceof FrameLayout.LayoutParams) {
                        FrameLayout.LayoutParams flp = (FrameLayout.LayoutParams) rawLp;
                        int wantBottom = dp(64) + ins.getSystemWindowInsetBottom();
                        if (flp.bottomMargin != wantBottom) {
                            flp.bottomMargin = wantBottom;
                            v.setLayoutParams(flp);
                        }
                    }
                    v.setPadding(
                        dp(18) + ins.getSystemWindowInsetLeft(),
                        dp(20) + ins.getSystemWindowInsetTop(),
                        dp(18) + ins.getSystemWindowInsetRight(),
                        dp(12));
                    return ins;
                }
            });
            bar.setOnApplyWindowInsetsListener(new android.view.View.OnApplyWindowInsetsListener() {
                @Override public android.view.WindowInsets onApplyWindowInsets(android.view.View v, android.view.WindowInsets ins) {
                    v.setPadding(
                        dp(16) + ins.getSystemWindowInsetLeft(),
                        dp(11),
                        dp(16) + ins.getSystemWindowInsetRight(),
                        dp(11) + ins.getSystemWindowInsetBottom());
                    return ins;
                }
            });
        }

        android.widget.LinearLayout head = new android.widget.LinearLayout(this);
        head.setOrientation(android.widget.LinearLayout.HORIZONTAL);
        head.setGravity(Gravity.CENTER_VERTICAL);
        dlTitleView = new TextView(this);
        dlTitleView.setText("下载管理");
        dlTitleView.setTextSize(19f);
        dlTitleView.setTypeface(null, android.graphics.Typeface.BOLD);
        head.addView(dlTitleView, new android.widget.LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        dlPanel.addView(head, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        dlTipView = new TextView(this);
        dlTipView.setText("自动双路并发保存至 内部存储/Download · 点击「打开播放」可调用播放器");
        dlTipView.setTextSize(12f);
        dlTipView.setPadding(0, dp(8), 0, dp(10));
        dlPanel.addView(dlTipView);

        android.widget.LinearLayout actBar = new android.widget.LinearLayout(this);
        actBar.setOrientation(android.widget.LinearLayout.HORIZONTAL);
        actBar.setGravity(Gravity.CENTER_VERTICAL);
        dlOpenBtn = mkBtn("打开目录", new android.view.View.OnClickListener() {
            @Override public void onClick(android.view.View v) { openDownloadFolder(); }
        });
        actBar.addView(dlOpenBtn, new android.widget.LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        android.widget.Space sp1 = new android.widget.Space(this);
        actBar.addView(sp1, new android.widget.LinearLayout.LayoutParams(dp(8), 1));
        dlClearDoneBtn = mkBtn("清空已完成", new android.view.View.OnClickListener() {
            @Override public void onClick(android.view.View v) { clearDoneTasks(); }
        });
        actBar.addView(dlClearDoneBtn, new android.widget.LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        dlPanel.addView(actBar, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        // 一键清理：把这台手机里本 App 下载过的文件全删掉（老版本留下的也在内）
        cleanBtn = mkBtn("清理本 App 下载的文件", new android.view.View.OnClickListener() {
            @Override public void onClick(android.view.View v) { confirmCleanAll(); }
        });
        android.widget.LinearLayout.LayoutParams cleanLp = new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        cleanLp.setMargins(0, dp(8), 0, dp(4));
        dlPanel.addView(cleanBtn, cleanLp);

        dlScroll = new android.widget.ScrollView(this);
        dlList = new android.widget.LinearLayout(this);
        dlList.setOrientation(android.widget.LinearLayout.VERTICAL);
        dlScroll.addView(dlList, new android.widget.FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        dlPanel.addView(dlScroll, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f));

        FrameLayout.LayoutParams dlRootLp = new FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT);
        dlRootLp.bottomMargin = dp(64);
        root.addView(dlPanel, dlRootLp);
        setContentView(root);
        applyNativeTheme();
        startEngine();
    }

    /**
     * App 被杀 / 崩溃时，MediaStore 里的 pending 行会留下 0 字节的半截文件。
     * 启动时把自己包名下还没完成的都删掉，免得 Download 目录越攒越脏。
     */
    private void cleanupStalePending() {
        if (Build.VERSION.SDK_INT < 29) return;
        try {
            android.database.Cursor c = getContentResolver().query(
                android.provider.MediaStore.Downloads.EXTERNAL_CONTENT_URI,
                new String[]{ android.provider.MediaStore.MediaColumns._ID },
                android.provider.MediaStore.MediaColumns.IS_PENDING + "=1 AND "
                    + android.provider.MediaStore.MediaColumns.OWNER_PACKAGE_NAME + "=?",
                new String[]{ getPackageName() }, null);
            if (c == null) return;
            java.util.List<Long> ids = new java.util.ArrayList<Long>();
            while (c.moveToNext()) ids.add(c.getLong(0));
            c.close();
            for (long id : ids) {
                Uri u = android.content.ContentUris.withAppendedId(
                    android.provider.MediaStore.Downloads.EXTERNAL_CONTENT_URI, id);
                try {
                    int n = getContentResolver().delete(u, null, null);
                    Log.i(TAG, "cleanup pending " + id + " -> " + n);
                } catch (Exception ignored) {}
            }
        } catch (Exception e) {
            Log.w(TAG, "cleanup pending failed", e);
        }
    }

    private int dp(int v) { return (int) (v * getResources().getDisplayMetrics().density); }

    /**
     * 取系统当前 DNS 传给引擎。Android 没有 /etc/resolv.conf，纯 Go 解析器在那边
     * 解析不出任何域名（表现就是 signer 退回 default + 所有接口 Get 失败）。
     */
    private String dnsServersEnv() {
        StringBuilder sb = new StringBuilder();
        try {
            android.net.ConnectivityManager cm =
                (android.net.ConnectivityManager) getSystemService(android.content.Context.CONNECTIVITY_SERVICE);
            android.net.Network net = cm.getActiveNetwork();
            if (net != null) {
                android.net.LinkProperties lp = cm.getLinkProperties(net);
                if (lp != null) {
                    for (java.net.InetAddress a : lp.getDnsServers()) {
                        String ip = a == null ? null : a.getHostAddress();
                        if (ip == null || ip.indexOf(':') >= 0) continue;   // 先只给 IPv4
                        if (sb.indexOf(ip) >= 0) continue;
                        if (sb.length() > 0) sb.append(',');
                        sb.append(ip).append(":53");
                    }
                }
            }
        } catch (Exception e) { Log.w(TAG, "dns lookup failed", e); }
        for (String s : new String[]{"223.5.5.5:53", "119.29.29.29:53"}) {
            if (sb.indexOf(s) >= 0) continue;
            if (sb.length() > 0) sb.append(',');
            sb.append(s);
        }
        return sb.toString();
    }

    // ---------------------------------------------------------------- 引擎

    private void startEngine() {
        new Thread(new Runnable() { public void run() {
            cleanupStalePending();
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
                String dns = dnsServersEnv();
                if (dns.length() > 0) pb.environment().put("YHKT_DNS", dns);
                Log.i(TAG, "YHKT_DNS=" + dns);
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
        // App 重启后引擎是全新的，但 WebView 里还留着上次的会话 cookie：
        // 直接把它们交给引擎，就能免登录继续用
        restoreFromCookies();

        String oldSt = tryHttp(BASE + "/api/status", null);
        String newSt = tryHttp(BASE + "/api/y2/status", null);
        boolean oldOk = oldSt != null && oldSt.indexOf("\"loggedIn\":true") >= 0;
        boolean newOk = newSt != null && newSt.indexOf("\"loggedIn\":true") >= 0;
        Log.i(TAG, "loggedIn old=" + oldOk + " y2=" + newOk);
        // 不再先跑登录页：直接进播放器。要登录就点页面里的按钮，登录完自动回来
        if (newOk) { openPlayer("/y2"); watchExpiry(); }
        else if (oldOk) { openPlayer("/"); watchExpiry(); }
        else openPlayer("/y2");
        pushStatus();
    }

    /** 把 WebView 里现存的会话 cookie 交回引擎（App 重启后免登录） */
    private void restoreFromCookies() {
        try {
            String ck = CookieManager.getInstance().getCookie(BASE);
            if (ck == null) return;
            String y2 = cookieValue(ck, "y2tok");
            if (y2 != null && y2.length() > 20) {
                String r = postLogin("/api/y2/login", "{\"jwt\":" + json(Uri.decode(y2)) + "}");
                Log.i(TAG, "restore y2 -> " + r);
            }
            String old = cookieValue(ck, "yht");
            if (old != null && old.length() > 20) {
                String r2 = postLogin("/api/login", "{\"token\":" + json(Uri.decode(old)) + "}");
                Log.i(TAG, "restore old -> " + r2);
            }
        } catch (Exception e) { Log.w(TAG, "restore failed", e); }
    }

    // ---------------------------------------------------------------- 登录（跳官网登录 → 回来自动生效）

    /**
     * 页面里的「登录」按钮都走这里：which = "y2"（新版）或 "old"（老平台）。
     * 只做一件事：跳到官网登录页，提示「登好按返回键回播放器」。
     * token 在后台静默截获并写进引擎，不弹进度、不弹结果，不打断用户。
     */
    @JavascriptInterface
    public void login(final String which) {
        final String w = "old".equals(which) ? "old" : "y2";
        ui.post(new Runnable() { public void run() { startLoginSite(w); } });
    }

    @JavascriptInterface
    public void clearAuth(final String which) {
        final boolean clearOld = which == null || which.isEmpty() || "old".equals(which) || "all".equals(which);
        final boolean clearY2 = which == null || which.isEmpty() || "y2".equals(which) || "all".equals(which);
        if (clearOld) { capturedOld = null; watchOldLogged = false; }
        if (clearY2) { capturedY2 = null; watchY2Logged = false; }
        ui.post(new Runnable() { public void run() {
            CookieManager cm = CookieManager.getInstance();
            synchronized (sessionCookies) {
                for (int i = sessionCookies.size() - 1; i >= 0; i--) {
                    String pair = sessionCookies.get(i);
                    if ((clearOld && pair.startsWith("yht=")) || (clearY2 && pair.startsWith("y2tok="))) {
                        sessionCookies.remove(i);
                    }
                }
            }
            if (clearOld) cm.setCookie(BASE, "yht=; Max-Age=0; Path=/");
            if (clearY2) cm.setCookie(BASE, "y2tok=; Max-Age=0; Path=/");
            cm.flush();
            pushStatus();
        }});
    }

    private void startLoginSite(final String which) {
        final boolean y2 = "y2".equals(which);
        String curUrl = web == null ? null : web.getUrl();
        if (curUrl != null && curUrl.startsWith(BASE)) lastPlayerUrl = curUrl;
        lastLoginTarget = which;
        pendingLogin = which;
        loginSiteSeen = false;
        loginRedirecting = false;
        forceNext = false;
        capturedOld = null; capturedY2 = null;
        bar.setOnClickListener(null);
        bar.setVisibility(TextView.GONE);
        if (loginTipView != null) {
            loginTipView.setText(y2 ? "请登录新版延河，成功后自动返回" : "请登录老平台，成功后自动返回");
        }
        if (loginBar != null) loginBar.setVisibility(android.view.View.VISIBLE);
        web.loadUrl(y2 ? Y2_SITE + "/portal/" : OLD_SITE);

        // 后台持续轮询直到成功捕获有效登录态，然后自动跳回播放器
        new Thread(new Runnable() { public void run() {
            long deadline = System.currentTimeMillis() + 300000L;
            String lastRejected = "";
            while (System.currentTimeMillis() < deadline && pendingLogin != null && !forceNext) {
                String tok = y2 ? waitY2Token(1800) : waitOldToken(1800);
                if (forceNext || pendingLogin == null) return;
                if (tok == null || tok.equals(lastRejected)) {
                    sleep(600);
                    continue;
                }
                String res = postLoginCandidates(y2 ? "/api/y2/login" : "/api/login", y2 ? "jwt" : "token", tok);
                Log.i(TAG, "login(" + which + ") -> " + res);
                boolean okRes = "OK".equals(res);
                boolean okR2 = false;
                if (!okRes && !y2) {
                    String r2 = postLoginCandidates("/api/y2/login", "jwt", tok);
                    Log.i(TAG, "login(old)->y2 -> " + r2);
                    okR2 = "OK".equals(r2);
                }
                if (!okRes && !okR2) {
                    lastRejected = tok;
                    sleep(800);
                    continue;
                }
                final boolean destY2 = y2 || okR2;
                ui.post(new Runnable() { public void run() {
                    if (loginRedirecting) return;
                    loginRedirecting = true;
                    pendingLogin = null;
                    loginSiteSeen = false;
                    forceNext = true;
                    if (loginTipView != null) loginTipView.setText("✓ 登录成功，正在自动返回…");
                    ui.postDelayed(new Runnable() { public void run() {
                        if (loginBar != null) loginBar.setVisibility(android.view.View.GONE);
                        ensureSessionCookies();
                        openPlayer(destY2 ? "/y2" : "/");
                        watchExpiry();
                    }}, 180);
                }});
                return;
            }
        }}).start();
    }

    /** 当前是不是停在播放器页面（而不是官网登录页） */
    private boolean onPlayerPage() {
        try {
            String u = web == null ? null : web.getUrl();
            return u != null && u.startsWith(BASE);
        } catch (Exception e) {
            return false;
        }
    }

    /** 从登录页回播放器（返回键 / 点顶部返回按钮）：回来之前再抓一次登录态，别等后台轮询 */
    private void backToPlayer() {
        final String which = pendingLogin != null ? pendingLogin : lastLoginTarget;
        final boolean y2 = "y2".equals(which);
        pendingLogin = null;
        loginSiteSeen = false;
        loginRedirecting = false;
        forceNext = true;              // 后台的等待循环可以停了
        bar.setOnClickListener(null);
        bar.setVisibility(TextView.GONE);
        if (loginBar != null) loginBar.setVisibility(android.view.View.GONE);
        new Thread(new Runnable() { public void run() {
            boolean useY2 = y2;
            if (which != null) {
                String tok = null;
                if (y2) {
                    String all = (capturedY2 == null ? "" : capturedY2 + "\n") + jsEval(Y2_TOKEN_JS);
                    if (all.length() > 40) tok = all;
                } else {
                    String all = (capturedOld == null ? "" : capturedOld + "\n") + jsEval(TOKEN_JS);
                    if (all.length() > 20) tok = all;
                }
                if (tok != null) {
                    String res = postLoginCandidates(y2 ? "/api/y2/login" : "/api/login", y2 ? "jwt" : "token", tok);
                    Log.i(TAG, "back-login(" + which + ") -> " + res);
                    if (!"OK".equals(res) && !y2) {
                        String r2 = postLoginCandidates("/api/y2/login", "jwt", tok);
                        if ("OK".equals(r2)) useY2 = true;
                    }
                }
            }
            final boolean destY2 = useY2;
            ui.post(new Runnable() { public void run() {
                ensureSessionCookies();
                openPlayer(destY2 ? "/y2" : "/");
                watchExpiry();
            }});
        }}).start();
    }

    /** 逐个候选试登录，全部失败时返回最后一次引擎返回的原文 */
    private String postLoginCandidates(String path, String key, String blob) {
        String last = "没有可用的候选 token";
        if (blob == null) return last;
        for (String c : blob.split("\n")) {
            c = c.trim();
            if (c.length() < 20) continue;
            String r = postLogin(path, "{\"" + key + "\":" + json(c) + "}");
            Log.i(TAG, path + " try " + c.substring(0, Math.min(18, c.length())) + "… => " + r);
            if (r != null && r.indexOf("\"ok\":true") >= 0) return "OK";
            last = r == null ? "请求失败" : r;
        }
        return last;
    }
    /** 两边都失败时把诊断画在屏幕上（截图就能发出去） */
    private void showDiag(final String err) {
        new Thread(new Runnable() { public void run() {
            final String where = jsEval("location.href");
            final String keys = jsEval(KEYS_JS);
            final String ck = jsEval("(document.cookie||'').slice(0,400)");
            final String st = tryHttp(BASE + "/api/status", null);
            final String st2 = tryHttp(BASE + "/api/y2/status", null);
            ui.post(new Runnable() { public void run() {
                StringBuilder h = new StringBuilder();
                String bg = darkTheme ? "#131314" : "#f8fafd";
                String fg = darkTheme ? "#e3e3e3" : "#1f1f1f";
                String dim = darkTheme ? "#c4c7c5" : "#444746";
                String card = darkTheme ? "#1e2025" : "#ffffff";
                String line = darkTheme ? "rgba(227,227,227,.14)" : "rgba(68,71,70,.16)";
                h.append("<html><head><meta name='viewport' content='width=device-width,initial-scale=1'>")
                 .append("<style>body{background:").append(bg).append(";color:").append(fg)
                 .append(";font:13px/1.6 -apple-system,system-ui;margin:0;padding:16px}")
                 .append("h3{font-size:13px;margin:14px 0 6px;color:").append(dim).append("}")
                 .append("pre{white-space:pre-wrap;word-break:break-all;background:").append(card)
                 .append(";border:1px solid ").append(line).append(";")
                 .append("border-radius:14px;padding:11px;font:11.5px/1.5 ui-monospace,Consolas,monospace;margin:0}</style></head><body>")
                 .append("<h3>引擎返回</h3><pre>").append(esc(err)).append("</pre>")
                 .append("<h3>当前页面</h3><pre>").append(esc(cut(where, 200))).append("</pre>")
                 .append("<h3>最近一次接口请求</h3><pre>").append(esc(cut(lastReqDump, 300))).append("</pre>")
                 .append("<h3>拦截到的请求头 token</h3><pre>old=").append(esc(cut(capturedOld, 40))).append("\ny2=").append(esc(cut(capturedY2, 40))).append("</pre>")
                 .append("<h3>页面存储的键名</h3><pre>").append(esc(keys)).append("</pre>")
                 .append("<h3>document.cookie</h3><pre>").append(esc(ck)).append("</pre>")
                 .append("<h3>本地 status</h3><pre>").append(esc(cut(st, 260))).append("\n")
                 .append(esc(cut(st2, 260))).append("</pre>")
                 .append("<p style='color:#6f7889'>把这一屏截图发给我就行。</p></body></html>");
                web.loadDataWithBaseURL(BASE, h.toString(), "text/html", "utf-8", null);
            }});
        }}).start();
    }

    private static String cut(String s, int n) {
        if (s == null) return "(null)";
        return s.length() > n ? s.substring(0, n) + "…" : s;
    }

    private static String esc(String s) {
        if (s == null) return "";
        return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;");
    }

    private String waitOldToken(int ms) {
        long t0 = System.currentTimeMillis();
        int tick = 0;
        while (System.currentTimeMillis() - t0 < ms) {
            if (forceNext) { forceNext = false; return null; }
            // 被重定向到新版的统一登录（aita）了：老平台这条路走不通，交给调用方回退
            if (capturedY2 != null) return null;
            // 0) 优先用从网络请求里截下来的 Authorization（最可信）
            if (capturedOld != null) return capturedOld + "\n" + (jsEval(TOKEN_JS) == null ? "" : jsEval(TOKEN_JS));
            // 1) 某些环境下老平台确实会种 yht cookie
            String ck = CookieManager.getInstance().getCookie(OLD_SITE);
            String tok = cookieValue(ck, "yht");
            if (tok != null && tok.length() > 20) return Uri.decode(tok);
            // 2) 主要还是 localStorage.auth —— 电脑版去读 leveldb 拿的就是这个
            String js = jsEval(TOKEN_JS);
            if (js != null && js.length() > 20) return js;
            sleep(450);
        }
        return null;
    }

    /** 在页面里同步取一个值（evaluateJavascript 是异步的，用 latch 等结果） */
    private String jsEval(String code) {
        final java.util.concurrent.CountDownLatch latch = new java.util.concurrent.CountDownLatch(1);
        final String[] out = new String[1];
        ui.post(new Runnable() { public void run() {
            try {
                web.evaluateJavascript(code, new android.webkit.ValueCallback<String>() {
                    @Override public void onReceiveValue(String v) { out[0] = v; latch.countDown(); }
                });
            } catch (Exception e) { latch.countDown(); }
        }});
        try { latch.await(5, java.util.concurrent.TimeUnit.SECONDS); } catch (InterruptedException ignored) {}
        String v = out[0];
        if (v == null || v.equals("null") || v.equals("undefined")) return "";
        try {
            Object o = new org.json.JSONTokener(v).nextValue();
            return o == null ? "" : String.valueOf(o);
        } catch (Exception e) {
            return v.replaceAll("^\"|\"$", "");
        }
    }

    private String waitY2Token(int ms) {
        long t0 = System.currentTimeMillis();
        int tick = 0;
        while (System.currentTimeMillis() - t0 < ms) {
            if (forceNext) { forceNext = false; return null; }
            // _token 是 PHP 序列化串，里面有分号 —— 当 cookie header 发会被截断，
            // 所以在页面里按官网 getToken 的逻辑把 JWT 抠出来
            String all = (capturedY2 == null ? "" : capturedY2 + "\n") + jsEval(Y2_TOKEN_JS);
            String tk = all;
            if (tk != null && tk.length() > 40) return tk;
            sleep(450);
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

    private void openPlayer(final String path) {
        ui.post(new Runnable() { public void run() {
            bar.setVisibility(TextView.GONE);
            if (loginBar != null) loginBar.setVisibility(android.view.View.GONE);
            // setCookie 是异步落盘的：先把会话 cookie 重新写一遍并读回确认，再导航，
            // 否则页面可能在 cookie 到位之前就请求 /api/status，显示"未登录"
            ensureSessionCookies();
            String target = BASE + path;
            String saved = lastPlayerUrl;
            if (saved != null && saved.startsWith(BASE)) {
                String rel = saved.substring(BASE.length());
                boolean wantY2 = "/y2".equals(path);
                boolean savedY2 = rel.startsWith("/y2");
                if (wantY2 == savedY2) {
                    target = saved;
                    lastPlayerUrl = null;
                }
            }
            web.loadUrl(target);
            ui.postDelayed(probe, 2500);
        }});
    }

    /** 24 小时登录态：登录成功之后才盯着它过期，没登录过就别弹"过期" */
    private void watchExpiry() {
        new Thread(new Runnable() { public void run() {
            String a = tryHttp(BASE + "/api/status", null);
            String b = tryHttp(BASE + "/api/y2/status", null);
            watchOldLogged = a != null && a.indexOf("\"loggedIn\":true") >= 0;
            watchY2Logged = b != null && b.indexOf("\"loggedIn\":true") >= 0;
            ui.post(new Runnable() { public void run() {
                ui.removeCallbacks(watch);
                if (watchOldLogged || watchY2Logged) ui.postDelayed(watch, 60000);
            }});
        }}).start();
    }

    /** 把登录拿到的 Set-Cookie 重新种一遍，并等它真正可读 */
    private void ensureSessionCookies() {
        java.util.List<String> snapshot;
        synchronized (sessionCookies) {
            snapshot = new java.util.ArrayList<String>(sessionCookies);
        }
        for (String pair : snapshot) {
            CookieManager.getInstance().setCookie(BASE, pair);
        }
        CookieManager.getInstance().flush();
        for (int i = 0; i < 10; i++) {
            String have = CookieManager.getInstance().getCookie(BASE);
            boolean all = true;
            for (String pair : snapshot) {
                String name = pair.split("=", 2)[0];
                if (have == null || !have.contains(name + "=")) { all = false; break; }
            }
            if (all || snapshot.isEmpty()) return;
            sleep(100);
        }
    }

    /** 登录状态交给页面显示（汉堡栏里的登录按钮），不在底栏弹字打扰 */
    private final Runnable probe = new Runnable() { public void run() {
        new Thread(new Runnable() { public void run() {
            String a = tryHttp(BASE + "/api/status", null);
            String b = tryHttp(BASE + "/api/y2/status", null);
            final boolean ao = a != null && a.indexOf("\"loggedIn\":true") >= 0;
            final boolean bo = b != null && b.indexOf("\"loggedIn\":true") >= 0;
            Log.i(TAG, "probe old=" + ao + " y2=" + bo + " cookies=" + CookieManager.getInstance().getCookie(BASE));
            pushStatus();
            ui.post(new Runnable() { public void run() {
                if (ao || bo) bar.setVisibility(TextView.GONE);
            }});
        }}).start();
    }};

    /** 登录态约 24 小时；过期后给一个"点一下重登"的入口 */
    private final Runnable watch = new Runnable() { public void run() {
        new Thread(new Runnable() { public void run() {
            String stY2 = watchY2Logged ? tryHttp(BASE + "/api/y2/status", null) : null;
            String stOld = watchOldLogged ? tryHttp(BASE + "/api/status", null) : null;
            final boolean y2Expired = watchY2Logged && (stY2 == null || stY2.indexOf("\"loggedIn\":true") < 0);
            final boolean oldExpired = watchOldLogged && (stOld == null || stOld.indexOf("\"loggedIn\":true") < 0);
            ui.post(new Runnable() { public void run() {
                if (!y2Expired && !oldExpired) {
                    if (watchY2Logged || watchOldLogged) ui.postDelayed(watch, 60000);
                    return;
                }
                final String target = y2Expired ? "y2" : "old";
                bar.setVisibility(TextView.VISIBLE);
                bar.setText(y2Expired ? "新版登录已过期 —— 点这里重新登录" : "老平台登录已过期 —— 点这里重新登录");
                bar.setOnClickListener(new android.view.View.OnClickListener() {
                    @Override public void onClick(android.view.View v) {
                        bar.setOnClickListener(null);
                        startLoginSite(target);
                    }
                });
            }});
        }}).start();
    }};

    // ---------------------------------------------------------------- 工具

    @JavascriptInterface
    public void setTheme(final String mode) {
        final boolean next = "dark".equalsIgnoreCase(mode);
        if (darkTheme == next && rootView != null) {
            ui.post(new Runnable() { public void run() { applySystemBars(); } });
            return;
        }
        darkTheme = next;
        ui.post(new Runnable() { public void run() { applyNativeTheme(); } });
    }

    private void applySystemBars() {
        if (customView != null) return;
        try {
            if (Build.VERSION.SDK_INT >= 21) {
                android.view.Window w = getWindow();
                w.addFlags(android.view.WindowManager.LayoutParams.FLAG_DRAWS_SYSTEM_BAR_BACKGROUNDS);
                w.setStatusBarColor(Color.parseColor(darkTheme ? "#1b1c1f" : "#f0f4f9"));
                w.setNavigationBarColor(Color.parseColor(darkTheme ? "#131314" : "#f8fafd"));
            }
            int flags = 0;
            if (!darkTheme && Build.VERSION.SDK_INT >= 23) flags |= android.view.View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
            if (!darkTheme && Build.VERSION.SDK_INT >= 26) flags |= 0x00000010; // SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR
            getWindow().getDecorView().setSystemUiVisibility(flags);
        } catch (Exception ignored) {}
    }

    private void applyNativeTheme() {
        applySystemBars();
        if (rootView != null) rootView.setBackgroundColor(Color.parseColor(darkTheme ? "#131314" : "#f8fafd"));
        if (loginBar != null) loginBar.setBackgroundColor(Color.parseColor(darkTheme ? "#1b1c1f" : "#f0f4f9"));
        if (loginTipView != null) loginTipView.setTextColor(Color.parseColor(darkTheme ? "#c4c7c5" : "#444746"));
        styleBtn(loginBackBtn, 2);
        if (bar != null) {
            bar.setBackgroundColor(Color.parseColor(darkTheme ? "#24262b" : "#d3e3fd"));
            bar.setTextColor(Color.parseColor(darkTheme ? "#e3e3e3" : "#041e49"));
        }
        if (dlPanel != null) dlPanel.setBackgroundColor(Color.parseColor(darkTheme ? "#131314" : "#f8fafd"));
        if (dlTitleView != null) dlTitleView.setTextColor(Color.parseColor(darkTheme ? "#e3e3e3" : "#1f1f1f"));
        if (dlTipView != null) dlTipView.setTextColor(Color.parseColor(darkTheme ? "#c4c7c5" : "#444746"));
        styleBtn(dlOpenBtn, 2);
        styleBtn(dlClearDoneBtn, 0);
        styleBtn(cleanBtn, 3);
        if (cleanBtn != null) cleanBtn.setAlpha(ownedCount > 0 ? 1f : 0.45f);
        rebuildDlRows();
    }

    private void exitFullscreen() {
        if (customView == null) return;
        try { rootView.removeView(customView); } catch (Exception ignored) {}
        customView = null;
        if (web != null) web.setVisibility(android.view.View.VISIBLE);
        getWindow().clearFlags(android.view.WindowManager.LayoutParams.FLAG_FULLSCREEN);
        setRequestedOrientation(android.content.pm.ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED);
        applySystemBars();
        if (customCb != null) { customCb.onCustomViewHidden(); customCb = null; }
    }

    private void hideSystemBars() {
        try {
            getWindow().getDecorView().setSystemUiVisibility(
                android.view.View.SYSTEM_UI_FLAG_FULLSCREEN
                | android.view.View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
                | android.view.View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY
                | android.view.View.SYSTEM_UI_FLAG_LAYOUT_STABLE);
        } catch (Exception ignored) {}
    }

    /** 把两个平台的登录状态推给页面，汉堡栏里的登录按钮据此显示「已登录 · 切换账号」 */
    private void pushStatus() {
        new Thread(new Runnable() { public void run() {
            String a = tryHttp(BASE + "/api/status", null);
            String b = tryHttp(BASE + "/api/y2/status", null);
            final int ao = a != null && a.indexOf("\"loggedIn\":true") >= 0 ? 1 : 0;
            final int bo = b != null && b.indexOf("\"loggedIn\":true") >= 0 ? 1 : 0;
            Log.i(TAG, "status push old=" + ao + " y2=" + bo);
            ui.post(new Runnable() { public void run() {
                if (web == null) return;
                try { web.evaluateJavascript("window.__yhktStat&&window.__yhktStat(" + ao + "," + bo + ")", null); } catch (Exception ignored) {}
            }});
        }}).start();
    }
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
                    String cname = pair.split("=", 2)[0];
                    synchronized (sessionCookies) {
                        for (int i = sessionCookies.size() - 1; i >= 0; i--) { if (sessionCookies.get(i).startsWith(cname + "=")) sessionCookies.remove(i); }
                        sessionCookies.add(pair);
                    }
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
        // 会话 cookie 在 CookieManager 里：不带上的话 /api/status 永远说"未登录"
        try {
            String ck = CookieManager.getInstance().getCookie(BASE);
            if (ck != null && ck.length() > 0) c.setRequestProperty("Cookie", ck);
        } catch (Exception ignored) {}
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

    // 老平台的 token 藏在 localStorage.auth 里（电脑版为此去读浏览器 leveldb），cookie 不保险
    // 老平台 token 藏在 localStorage 里（电脑版为此去读浏览器 leveldb）。
    // 键名各版本不同，而且同一页面里可能同时存在新版平台的 JWT，所以返回一串候选，
    // 交给原生逐个试 —— 哪个能通过 /api/login 就用哪个。
    private static final String TOKEN_JS =
        "(function(){var out=[];"
        + "function add(v){if(!v)return;v=String(v);"
        + "var m=/(eyJ[\\w-]+\\.[\\w-]+\\.[\\w-]*)/.exec(v);"
        + "if(m){if(out.indexOf(m[1])<0)out.push(m[1]);return;}"
        + "if(v.length>40&&v.indexOf(' ')<0&&out.indexOf(v)<0)out.push(v);}"
        + "function scan(st){try{for(var i=0;i<st.length;i++){var k=st.key(i),v=st.getItem(k)||'';"
        + "if(/token|auth|user/i.test(k)){add(v);try{var o=JSON.parse(v);"
        + "if(o){add(o.token);add(o.accessToken);if(o.auth)add(o.auth.token);}}catch(e){}}}}catch(e){}}"
        + "scan(localStorage);scan(sessionStorage);"
        + "return out.join(String.fromCharCode(10));})()";

    // 新版平台：_token 是 PHP 序列化串（含分号，当 cookie header 会截断），
    // 按官网 getToken 的正则抠 JWT；同样返回候选列表。
    private static final String Y2_TOKEN_JS =
        "(function(){var out=[];"
        + "function add(v){if(!v)return;v=String(v);"
        + "var m=/\\{i:\\d+;s:\\d+:\"_token\";i:\\d+;s:\\d+:\"(.+?)\";\\}/.exec(v);"
        + "if(m&&out.indexOf(m[1])<0)out.push(m[1]);"
        + "var j=/(eyJ[\\w-]+\\.[\\w-]+\\.[\\w-]*)/.exec(v);"
        + "if(j&&out.indexOf(j[1])<0)out.push(j[1]);}"
        + "var ck='';try{ck=document.cookie||''}catch(e){}"
        + "add(ck);"
        + "try{add(localStorage.getItem('_token')||'')}catch(e){}"
        + "try{add(localStorage.getItem('token')||'')}catch(e){}"
        + "return out.join(String.fromCharCode(10));})()";

    // 诊断用：把当前页面的存储键名列出来
    private static final String KEYS_JS =
        "(function(){var o=[];try{for(var i=0;i<localStorage.length;i++){o.push('LS:'+localStorage.key(i));}}catch(e){}"
        + "try{for(var j=0;j<sessionStorage.length;j++){o.push('SS:'+sessionStorage.key(j));}}catch(e){}"
        + "try{o.push('CK:'+(document.cookie||'').split(';').map(function(x){return x.split('=')[0].trim()}).join(','));}catch(e){}"
        + "return o.join('  ');})()";
    // ---------------------------------------------------------------- 下载
    // ---------------------------------------------------------------- 下载

    /** 一个下载任务：支持排队、双路并发、实时速度与自动重试 */
    static class DlTask {
        String url = "";
        String name = "准备中…";
        String mime = "video/mp4";
        volatile long done = 0, total = -1, speedBps = 0;
        volatile boolean running = false, cancel = false, doneOk = false, failed = false;
        volatile boolean fromBatch = false;
        volatile String error = null;
        volatile HttpURLConnection conn = null;
        volatile Uri target = null;     // Android 10+：MediaStore 里的那一行
        volatile File file = null;      // Android 9 及以下：直接写出去的文件
        TextView line = null;
        android.widget.ProgressBar bar = null;
    }

    private static String fmtMB(long bytes) {
        return String.format(java.util.Locale.US, "%.1f MB", bytes / 1048576.0);
    }

    private static String fmtSpeed(long bps) {
        if (bps <= 0) return "";
        if (bps >= 1024L * 1024L) return String.format(java.util.Locale.US, " · %.1f MB/s", bps / 1048576.0);
        return String.format(java.util.Locale.US, " · %d KB/s", Math.max(1L, bps / 1024L));
    }

    private final java.util.List<DlTask> tasks = new java.util.concurrent.CopyOnWriteArrayList<DlTask>();
    private final java.util.concurrent.ExecutorService dlPool = java.util.concurrent.Executors.newFixedThreadPool(2);
    private volatile DlTask lastTask = null;

    /**
     * 页面里点「下载」会 window.open('/y2dl?...')；WebView 处理不了这种流，
     * 这里接过来：带上网关 cookie 直接拉，存进系统「下载」目录。
     */
    @JavascriptInterface
    public void download(final String url) {
        Log.i(TAG, "download " + url);
        if (Build.VERSION.SDK_INT < 29 && checkSelfPermission(android.Manifest.permission.WRITE_EXTERNAL_STORAGE) != 0) {
            pendingUrl = url;
            requestPermissions(new String[]{android.Manifest.permission.WRITE_EXTERNAL_STORAGE}, 41);
            return;
        }
        startDownload(url, null, false, true);
    }

    /** 批量下载 / 双画分别保存：接收前端传来的 [{url, title}, ...] JSON，走 2 路并发队列 */
    @JavascriptInterface
    public void downloadBatchJson(final String jsonStr) {
        Log.i(TAG, "downloadBatchJson len=" + (jsonStr == null ? 0 : jsonStr.length()));
        if (jsonStr == null || jsonStr.trim().isEmpty()) return;
        if (Build.VERSION.SDK_INT < 29 && checkSelfPermission(android.Manifest.permission.WRITE_EXTERNAL_STORAGE) != 0) {
            pendingBatchJson = jsonStr;
            requestPermissions(new String[]{android.Manifest.permission.WRITE_EXTERNAL_STORAGE}, 43);
            return;
        }
        enqueueBatchJson(jsonStr);
    }

    @JavascriptInterface
    public void openDownloadDir(final String dir) {
        openDownloadFolder();
    }

    private void enqueueBatchJson(final String jsonStr) {
        try {
            org.json.JSONArray arr = new org.json.JSONArray(jsonStr);
            int added = 0;
            for (int i = 0; i < arr.length(); i++) {
                org.json.JSONObject obj = arr.optJSONObject(i);
                if (obj == null) continue;
                String u = obj.optString("url", "").trim();
                if (u.isEmpty()) continue;
                if (u.startsWith("/")) u = BASE + u;
                String title = obj.optString("title", "").trim();
                startDownload(u, title, true, false);
                added++;
            }
            if (added > 0) {
                final int count = added;
                ui.post(new Runnable() { public void run() {
                    Toast.makeText(MainActivity.this,
                        count > 1 ? ("已加入 " + count + " 个下载任务（双路并发排队下载）") : "已开始下载任务",
                        Toast.LENGTH_SHORT).show();
                    if (count <= 2) openDlPanel();
                }});
            }
        } catch (Exception e) {
            Log.e(TAG, "enqueueBatchJson failed", e);
            ui.post(new Runnable() { public void run() {
                Toast.makeText(MainActivity.this, "加入批量下载失败：" + e.getMessage(), Toast.LENGTH_SHORT).show();
            }});
        }
    }

    @Override public void onRequestPermissionsResult(int req, String[] perms, int[] granted) {
        super.onRequestPermissionsResult(req, perms, granted);
        if (req == 41 && pendingUrl != null) {
            String u = pendingUrl; pendingUrl = null;
            if (granted.length > 0 && granted[0] == 0) startDownload(u, null, false, true);
            else say("没有存储权限，下载不了");
        } else if (req == 43 && pendingBatchJson != null) {
            String bj = pendingBatchJson; pendingBatchJson = null;
            if (granted.length > 0 && granted[0] == 0) enqueueBatchJson(bj);
            else say("没有存储权限，下载不了");
        }
    }

    private void startDownload(final String url, final String hintTitle, final boolean fromBatch, final boolean openPanelNow) {
        final DlTask t = new DlTask();
        t.url = url;
        t.fromBatch = fromBatch;
        if (hintTitle != null && !hintTitle.isEmpty()) {
            t.name = sanitizeName(hintTitle);
        } else {
            String parsedTitle = queryParam(url, "title");
            if (parsedTitle != null && !parsedTitle.isEmpty()) t.name = sanitizeName(parsedTitle);
        }
        tasks.add(t);
        lastTask = t;
        updateChip();
        if (openPanelNow) openDlPanel();
        else if (dlPanel != null && dlPanel.getVisibility() == android.view.View.VISIBLE) {
            ui.post(new Runnable() { public void run() { rebuildDlRows(); } });
        }
        submitDownloadTask(t);
    }

    private void submitDownloadTask(final DlTask t) {
        dlPool.submit(new Runnable() { public void run() { runDownloadWithRetry(t); } });
    }

    private void runDownloadWithRetry(final DlTask t) {
        if (t.cancel) return;
        t.running = true;
        t.failed = false;
        t.error = null;
        refreshTaskRow(t);
        for (int attempt = 0; attempt < 3; attempt++) {
            if (t.cancel) break;
            if (attempt > 0) {
                t.done = 0; t.total = -1; t.speedBps = 0;
                t.failed = false; t.error = null;
                t.target = null; t.file = null;
                refreshTaskRow(t);
                sleep(1200L * attempt);
                if (t.cancel) break;
            }
            boolean retryable = runDownloadOnce(t);
            if (t.doneOk || t.cancel || !retryable) break;
        }
        t.running = false;
        t.speedBps = 0;
        final String fName = t.name;
        final long total = t.done;
        boolean leftover = false;
        if (!t.doneOk && (t.cancel || t.failed)) leftover = hardDelete(t);
        final boolean fLeft = leftover;
        if (t.cancel) {
            tasks.remove(t);
            ui.post(new Runnable() { public void run() { rebuildDlRows(); } });
            updateChip();
        } else {
            refreshTaskRow(t);
            updateChip();
        }
        final boolean batchAllDone = t.doneOk && t.fromBatch && activeCount() == 0;
        ui.post(new Runnable() { public void run() {
            if (t.doneOk) {
                if (!t.fromBatch) {
                    Toast.makeText(MainActivity.this,
                        "下载完成：" + fName + "\n" + fmtMB(total) + " → 内部存储/Download",
                        Toast.LENGTH_LONG).show();
                } else if (batchAllDone) {
                    Toast.makeText(MainActivity.this,
                        "全部下载任务已完成 → 内部存储/Download",
                        Toast.LENGTH_LONG).show();
                }
                refreshOwnedCount();
            } else if (t.cancel) {
                if (fLeft) {
                    Toast.makeText(MainActivity.this,
                        "已取消。部分缓存可能尚未清理，可点「打开目录」查看",
                        Toast.LENGTH_LONG).show();
                    showLeftoverHint();
                } else if (!t.fromBatch) {
                    Toast.makeText(MainActivity.this, "已取消下载并清理临时文件", Toast.LENGTH_SHORT).show();
                }
            } else if (!t.fromBatch) {
                say("下载失败：" + t.error);
            }
        }});
    }

    /** 执行一次下载；返回 true 表示属于可重试的瞬时网络中断 */
    private boolean runDownloadOnce(final DlTask t) {
        HttpURLConnection c = null;
        OutputStream os = null;
        try {
            c = (HttpURLConnection) new URL(t.url).openConnection();
            t.conn = c;
            String cookie = CookieManager.getInstance().getCookie(BASE);
            if (cookie != null) c.setRequestProperty("Cookie", cookie);
            c.setConnectTimeout(10000);
            c.setReadTimeout(180000);
            c.setRequestProperty("Accept-Encoding", "identity");
            int code = c.getResponseCode();
            if (code >= 400) {
                t.failed = true;
                t.error = "HTTP " + code;
                return code >= 500 || code == 429;
            }
            t.total = c.getContentLength();
            String name = fileName(c.getHeaderField("content-disposition"));
            if (name == null || name.isEmpty()) {
                String base = (t.name != null && !t.name.equals("准备中…")) ? t.name : ("lecture_" + System.currentTimeMillis());
                if (!base.endsWith(".mp4") && !base.endsWith(".ts")) {
                    base += t.url.contains("/dl?") ? ".ts" : ".mp4";
                }
                name = base;
            }
            name = sanitizeName(name);
            t.name = name;
            String mime = name.endsWith(".ts") ? "video/mp2t" : "video/mp4";
            t.mime = mime;
            if (Build.VERSION.SDK_INT >= 29) {
                android.content.ContentValues cv = new android.content.ContentValues();
                cv.put(android.provider.MediaStore.MediaColumns.DISPLAY_NAME, name);
                cv.put(android.provider.MediaStore.MediaColumns.MIME_TYPE, mime);
                cv.put(android.provider.MediaStore.MediaColumns.RELATIVE_PATH, android.os.Environment.DIRECTORY_DOWNLOADS);
                cv.put(android.provider.MediaStore.MediaColumns.IS_PENDING, 1);
                t.target = getContentResolver().insert(android.provider.MediaStore.Downloads.EXTERNAL_CONTENT_URI, cv);
                if (t.target == null) throw new java.io.IOException("建不了下载文件");
                os = getContentResolver().openOutputStream(t.target);
            } else {
                File dir = android.os.Environment.getExternalStoragePublicDirectory(android.os.Environment.DIRECTORY_DOWNLOADS);
                if (!dir.exists()) dir.mkdirs();
                t.file = uniqueLegacyFile(dir, name);
                os = new java.io.FileOutputStream(t.file);
            }
            if (os == null) throw new java.io.IOException("写不了文件");
            java.io.InputStream in = c.getInputStream();
            byte[] buf = new byte[64 * 1024];
            long lastPush = 0, winAt = System.currentTimeMillis(), winBytes = 0;
            int n;
            while ((n = in.read(buf)) > 0) {
                if (t.cancel) throw new java.io.IOException("已取消");
                os.write(buf, 0, n);
                t.done += n;
                long now = System.currentTimeMillis();
                if (now - winAt >= 800) {
                    t.speedBps = (t.done - winBytes) * 1000L / Math.max(1L, now - winAt);
                    winAt = now; winBytes = t.done;
                }
                if (now - lastPush > 400) { lastPush = now; refreshTaskRow(t); }
            }
            if (t.cancel) throw new java.io.IOException("已取消");
            os.flush();
            t.doneOk = true;
            if (Build.VERSION.SDK_INT >= 29 && t.target != null) {
                android.content.ContentValues done = new android.content.ContentValues();
                done.put(android.provider.MediaStore.MediaColumns.IS_PENDING, 0);
                try { getContentResolver().update(t.target, done, null, null); } catch (Exception ignored) {}
            }
            return false;
        } catch (Exception e) {
            if (t.cancel) {
                t.error = "已取消";
                return false;
            }
            t.failed = true;
            t.error = e.getMessage();
            Log.e(TAG, "download failed", e);
            return (e instanceof java.io.IOException);
        } finally {
            try { if (os != null) os.close(); } catch (Exception ignored) {}
            if (c != null) c.disconnect();
            t.conn = null;
            if (!t.doneOk && !t.cancel) hardDelete(t);
        }
    }

    private static String sanitizeName(String s) {
        if (s == null) return "lecture";
        String out = s.replaceAll("[\\\\/:*?\"<>|\\x00-\\x1f]", "_").trim();
        return out.isEmpty() ? "lecture" : out;
    }

    private static String queryParam(String rawUrl, String key) {
        try {
            Uri u = Uri.parse(rawUrl);
            return u.getQueryParameter(key);
        } catch (Exception e) {
            return null;
        }
    }

    private static File uniqueLegacyFile(File dir, String name) {
        File f = new File(dir, name);
        if (!f.exists()) return f;
        int dot = name.lastIndexOf('.');
        String stem = dot > 0 ? name.substring(0, dot) : name;
        String ext = dot > 0 ? name.substring(dot) : "";
        for (int i = 1; i < 100; i++) {
            File alt = new File(dir, stem + " (" + i + ")" + ext);
            if (!alt.exists()) return alt;
        }
        return f;
    }

    /** 取消后系统没让删干净：留一条能直接跳到下载目录的提示 */
    private void showLeftoverHint() {
        bar.setVisibility(TextView.VISIBLE);
        bar.setText("取消后还留着半截文件 —— 点这里打开「下载」目录删掉");
        bar.setOnClickListener(new android.view.View.OnClickListener() {
            @Override public void onClick(android.view.View v) {
                bar.setOnClickListener(null);
                bar.setVisibility(TextView.GONE);
                openDownloadFolder();
            }
        });
    }

    /**
     * 把没下完的文件彻底删掉。MediaStore 的行删掉就等于删文件；
     * Android 9 及以下是自己 new File 写的，直接 delete。
     */
    private boolean hardDelete(DlTask t) {
        Uri u = t.target;
        if (u != null) {
            int n = -1;
            try { n = getContentResolver().delete(u, null, null); } catch (Exception e) { Log.w(TAG, "delete row failed", e); }
            if (n <= 0 && Build.VERSION.SDK_INT >= 29) {
                // 极端情况：行被系统锁住，先把 pending 清掉再删一次
                try {
                    android.content.ContentValues cv = new android.content.ContentValues();
                    cv.put(android.provider.MediaStore.MediaColumns.IS_PENDING, 0);
                    getContentResolver().update(u, cv, null, null);
                    n = getContentResolver().delete(u, null, null);
                } catch (Exception ignored) {}
            }
            Log.i(TAG, "delete mediastore row -> " + n);
            t.target = null;
        }
        File f = t.file;
        if (f != null && f.exists()) {
            boolean ok = f.delete();
            Log.i(TAG, "delete file " + f.getAbsolutePath() + " -> " + ok);
            t.file = null;
        }
        // 回头验一遍：还在不在（有些 ROM 删了也留着影子，得靠这个判断告诉用户）
        boolean left = false;
        if (u != null) {
            android.database.Cursor c = null;
            try {
                c = getContentResolver().query(u, new String[]{"_id"}, null, null, null);
                if (c != null && c.getCount() > 0) left = true;
            } catch (Exception ignored) {
            } finally { if (c != null) try { c.close(); } catch (Exception ignored) {} }
        }
        if (f != null && f.exists()) left = true;
        return left;
    }

    // ---- 下载面板 UI ----

    /** 页面汉堡栏里可以调这个打开下载面板 */
    @JavascriptInterface
    public void openDownloads() {
        openDlPanel();
    }

    @JavascriptInterface
    public void closeDownloads() {
        closeDlPanel(false);
    }

    private void openDlPanel() {
        ui.post(new Runnable() { public void run() {
            if (dlPanel == null) return;
            rebuildDlRows();
            dlPanel.setVisibility(android.view.View.VISIBLE);
            if (web != null) {
                try { web.evaluateJavascript("window.__yhktDlOpened&&window.__yhktDlOpened()", null); } catch (Exception ignored) {}
            }
        }});
        refreshOwnedCount();
    }

    /** 清空已完成 / 失败的任务记录（不删磁盘文件） */
    private void clearDoneTasks() {
        int removed = 0;
        java.util.Iterator<DlTask> it = tasks.iterator();
        java.util.List<DlTask> drop = new java.util.ArrayList<DlTask>();
        while (it.hasNext()) {
            DlTask t = it.next();
            if (t.doneOk || t.failed || t.cancel) { drop.add(t); removed++; }
        }
        if (removed == 0) {
            Toast.makeText(this, "没有可清理的已完成或失败记录", Toast.LENGTH_SHORT).show();
            return;
        }
        tasks.removeAll(drop);
        rebuildDlRows();
        updateChip();
        Toast.makeText(this, "已清空 " + removed + " 条记录（文件仍保留在下载目录）", Toast.LENGTH_SHORT).show();
    }

    /** 数一下本 App 在 Downloads 里还留着多少个文件（老版本留下的也算） */
    private void refreshOwnedCount() {
        new Thread(new Runnable() { public void run() {
            final int n = ownedRows().size();
            ownedCount = n;
            ui.post(new Runnable() { public void run() {
                if (cleanBtn == null) return;
                cleanBtn.setText(n > 0 ? "清理本 App 下载的文件（" + n + " 个）" : "清理本 App 下载的文件");
                cleanBtn.setEnabled(n > 0);
                cleanBtn.setAlpha(n > 0 ? 1f : 0.45f);
            }});
        }}).start();
    }

    /** 本 App 创建、还留在 Downloads 里的那些行（Android 10+ 才有 owner 信息） */
    private java.util.List<Uri> ownedRows() {
        java.util.List<Uri> out = new java.util.ArrayList<Uri>();
        if (Build.VERSION.SDK_INT < 29) return out;
        android.database.Cursor c = null;
        try {
            c = getContentResolver().query(
                android.provider.MediaStore.Downloads.EXTERNAL_CONTENT_URI,
                new String[]{ android.provider.MediaStore.MediaColumns._ID },
                android.provider.MediaStore.MediaColumns.OWNER_PACKAGE_NAME + "=?",
                new String[]{ getPackageName() }, null);
            if (c != null) {
                while (c.moveToNext()) {
                    out.add(android.content.ContentUris.withAppendedId(
                        android.provider.MediaStore.Downloads.EXTERNAL_CONTENT_URI, c.getLong(0)));
                }
            }
        } catch (Exception e) {
            Log.w(TAG, "query owned rows failed", e);
        } finally {
            if (c != null) try { c.close(); } catch (Exception ignored) {}
        }
        return out;
    }

    private void confirmCleanAll() {
        int running = activeCount();
        if (running > 0) { Toast.makeText(this, "还有 " + running + " 个下载在跑，先等下完或取消再清理", Toast.LENGTH_SHORT).show(); return; }
        if (ownedCount <= 0) { refreshOwnedCount(); return; }
        new android.app.AlertDialog.Builder(this)
            .setTitle("清理下载文件")
            .setMessage("把这个 App 在「下载」里留下的 " + ownedCount + " 个文件全部删掉？\n\n"
                + "包括以前版本没下完的残片。删了要重新下。")
            .setNegativeButton("算了", null)
            .setPositiveButton("全删掉", new android.content.DialogInterface.OnClickListener() {
                @Override public void onClick(android.content.DialogInterface d, int which) { cleanAllOwned(); }
            })
            .show();
    }

    private void cleanAllOwned() {
        new Thread(new Runnable() { public void run() {
            int n = 0;
            for (Uri u : ownedRows()) {
                try { if (getContentResolver().delete(u, null, null) > 0) n++; } catch (Exception ignored) {}
            }
            // 列表清空（文件都没了，行也没意义）
            tasks.clear();
            final int fn = n;
            ownedCount = 0;
            ui.post(new Runnable() { public void run() {
                rebuildDlRows();
                updateChip();
                if (cleanBtn != null) { cleanBtn.setText("清理本 App 下载的文件"); cleanBtn.setEnabled(false); cleanBtn.setAlpha(0.45f); }
                Toast.makeText(MainActivity.this, "已清理 " + fn + " 个文件", Toast.LENGTH_SHORT).show();
            }});
        }}).start();
    }

    private void closeDlPanel() {
        closeDlPanel(true);
    }

    private void closeDlPanel(final boolean notifyWeb) {
        ui.post(new Runnable() { public void run() {
            if (dlPanel != null) dlPanel.setVisibility(android.view.View.GONE);
            if (notifyWeb && web != null) {
                try { web.evaluateJavascript("window.__yhktDlClosed&&window.__yhktDlClosed()", null); } catch (Exception ignored) {}
            }
        }});
        updateChip();
    }

    private int activeCount() {
        int n = 0;
        for (DlTask t : tasks) if (!t.doneOk && !t.failed && !t.cancel) n++;
        return n;
    }

    /** 没有浮动入口了：把任务数推给页面，汉堡栏里的「下载管理」自己显示 (n) */
    private void updateChip() {
        final int n = activeCount();
        ui.post(new Runnable() { public void run() {
            if (web == null) return;
            try { web.evaluateJavascript("window.__yhktDl&&window.__yhktDl(" + n + ")", null); } catch (Exception ignored) {}
        }});
    }

    /** 下完的那条点「打开」→ 直接调系统播放器播放该文件，失败再退回目录 */
    private void openDownloadedTask(DlTask t) {
        if (t != null && t.target != null) {
            try {
                Intent view = new Intent(Intent.ACTION_VIEW);
                view.setDataAndType(t.target, t.mime != null ? t.mime : "video/*");
                view.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_ACTIVITY_NEW_TASK);
                startActivity(Intent.createChooser(view, "选择播放器打开"));
                return;
            } catch (Exception ignored) {}
        }
        if (t != null && t.file != null && t.file.exists() && Build.VERSION.SDK_INT < 24) {
            try {
                Intent view = new Intent(Intent.ACTION_VIEW);
                view.setDataAndType(Uri.fromFile(t.file), t.mime != null ? t.mime : "video/*");
                view.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                startActivity(Intent.createChooser(view, "选择播放器打开"));
                return;
            } catch (Exception ignored) {}
        }
        openDownloadFolder();
    }

    /** 进系统「下载」目录 */
    private void openDownloadFolder() {
        try {
            Intent i = new Intent(android.app.DownloadManager.ACTION_VIEW_DOWNLOADS);
            i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
            startActivity(i);
        } catch (Exception e) {
            say("打不开系统下载目录：" + e.getMessage());
        }
    }

    private void rebuildDlRows() {
        if (dlList == null) return;
        dlList.removeAllViews();
        if (tasks.isEmpty()) {
            TextView tv = new TextView(this);
            tv.setText("还没有下载任务。播放界面点「下载」或打开「批量下载」即可自动排队保存。");
            tv.setTextColor(Color.parseColor(darkTheme ? "#c4c7c5" : "#444746"));
            tv.setTextSize(13f);
            tv.setPadding(dp(6), dp(22), dp(6), dp(22));
            dlList.addView(tv);
            return;
        }
        for (DlTask t : tasks) dlList.addView(rowOf(t));
    }

    private void styleBtn(android.widget.Button b, int variant) {
        if (b == null) return;
        int bg, fg, stroke;
        if (variant == 1) { // M3 Primary pill
            bg = Color.parseColor(darkTheme ? "#a8c7fa" : "#0b57d0");
            fg = Color.parseColor(darkTheme ? "#062e6f" : "#ffffff");
            stroke = bg;
        } else if (variant == 2) { // M3 Tonal pill
            bg = Color.parseColor(darkTheme ? "#0842a0" : "#d3e3fd");
            fg = Color.parseColor(darkTheme ? "#d3e3fd" : "#041e49");
            stroke = bg;
        } else if (variant == 3) { // M3 Error tonal pill
            bg = Color.parseColor(darkTheme ? "#601410" : "#f9dedc");
            fg = Color.parseColor(darkTheme ? "#f9dedc" : "#410e0b");
            stroke = bg;
        } else { // M3 Secondary / Outlined pill
            bg = Color.parseColor(darkTheme ? "#24262b" : "#e9eef6");
            fg = Color.parseColor(darkTheme ? "#e3e3e3" : "#1f1f1f");
            stroke = Color.parseColor(darkTheme ? "#33e3e3e3" : "#26444746");
        }
        android.graphics.drawable.GradientDrawable gd = new android.graphics.drawable.GradientDrawable();
        gd.setColor(bg);
        gd.setCornerRadius(dp(999));
        if (variant == 0) gd.setStroke(dp(1), stroke);
        b.setBackground(gd);
        b.setTextColor(fg);
    }

    private android.widget.Button mkBtn(String text, android.view.View.OnClickListener l) {
        return mkBtn(text, 0, l);
    }

    private android.widget.Button mkBtn(String text, int variant, android.view.View.OnClickListener l) {
        android.widget.Button b = new android.widget.Button(this);
        b.setText(text);
        b.setTextSize(12.5f);
        b.setAllCaps(false);
        b.setPadding(dp(16), dp(6), dp(16), dp(6));
        b.setMinimumHeight(dp(36));
        b.setMinHeight(dp(36));
        styleBtn(b, variant);
        b.setOnClickListener(l);
        return b;
    }

    private android.view.View rowOf(final DlTask t) {
        android.widget.LinearLayout box = new android.widget.LinearLayout(this);
        box.setOrientation(android.widget.LinearLayout.VERTICAL);
        box.setPadding(dp(14), dp(12), dp(14), dp(12));
        android.graphics.drawable.GradientDrawable cardBg = new android.graphics.drawable.GradientDrawable();
        cardBg.setColor(Color.parseColor(darkTheme ? "#1e2025" : "#ffffff"));
        cardBg.setCornerRadius(dp(18));
        cardBg.setStroke(dp(1), Color.parseColor(darkTheme ? "#1ee3e3e3" : "#24444746"));
        box.setBackground(cardBg);
        android.widget.LinearLayout.LayoutParams cardLp = new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        cardLp.setMargins(0, dp(10), 0, 0);
        box.setLayoutParams(cardLp);

        TextView line = new TextView(this);
        line.setTextColor(Color.parseColor(darkTheme ? "#e3e3e3" : "#1f1f1f"));
        line.setTextSize(13f);
        line.setLineSpacing(dp(3), 1.0f);
        t.line = line;
        box.addView(line, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        android.widget.ProgressBar pb = new android.widget.ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal);
        pb.setMax(100);
        if (Build.VERSION.SDK_INT >= 21) {
            pb.setProgressTintList(android.content.res.ColorStateList.valueOf(
                Color.parseColor(darkTheme ? "#a8c7fa" : "#0b57d0")));
        }
        t.bar = pb;
        android.widget.LinearLayout.LayoutParams pbLp = new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, dp(6));
        pbLp.setMargins(0, dp(8), 0, dp(10));
        box.addView(pb, pbLp);

        android.widget.LinearLayout row = new android.widget.LinearLayout(this);
        row.setOrientation(android.widget.LinearLayout.HORIZONTAL);
        final boolean active = !t.doneOk && !t.failed && !t.cancel;
        if (active) {
            row.addView(mkBtn("取消", 3, new android.view.View.OnClickListener() {
                @Override public void onClick(android.view.View v) { cancelTask(t); }
            }));
        } else if (t.doneOk) {
            row.addView(mkBtn("打开播放", 1, new android.view.View.OnClickListener() {
                @Override public void onClick(android.view.View v) { openDownloadedTask(t); }
            }));
            android.widget.Space sp = new android.widget.Space(this);
            row.addView(sp, new android.widget.LinearLayout.LayoutParams(dp(8), 1));
            row.addView(mkBtn("删除", 3, new android.view.View.OnClickListener() {
                @Override public void onClick(android.view.View v) { confirmDelete(t); }
            }));
        } else if (t.failed) {
            row.addView(mkBtn("重试", 2, new android.view.View.OnClickListener() {
                @Override public void onClick(android.view.View v) {
                    t.failed = false; t.error = null; t.done = 0; t.total = -1; t.speedBps = 0;
                    t.running = false;
                    rebuildDlRows();
                    updateChip();
                    submitDownloadTask(t);
                }
            }));
            android.widget.Space sp = new android.widget.Space(this);
            row.addView(sp, new android.widget.LinearLayout.LayoutParams(dp(8), 1));
            row.addView(mkBtn("移除", 0, new android.view.View.OnClickListener() {
                @Override public void onClick(android.view.View v) {
                    tasks.remove(t);
                    rebuildDlRows();
                    updateChip();
                }
            }));
        } else {
            row.addView(mkBtn("移除", 0, new android.view.View.OnClickListener() {
                @Override public void onClick(android.view.View v) {
                    tasks.remove(t);
                    rebuildDlRows();
                    updateChip();
                }
            }));
        }
        box.addView(row, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        fillRowText(t);
        return box;
    }

    /** 取消：立刻从列表里拿掉，文件中途删 + 线程收尾时再删一次 */
    private void cancelTask(final DlTask t) {
        if (t.doneOk || t.failed || t.cancel) return;      // 点下去这一刻再判断，别误删刚下完的
        t.cancel = true;
        HttpURLConnection c = t.conn;
        if (c != null) { try { c.disconnect(); } catch (Exception ignored) {} }
        tasks.remove(t);
        rebuildDlRows();
        updateChip();
        Toast.makeText(this, "正在取消下载…", Toast.LENGTH_SHORT).show();
    }

    private void confirmDelete(final DlTask t) {
        new android.app.AlertDialog.Builder(this)
            .setTitle("删除文件？")
            .setMessage(t.name + "\n\n文件会从「下载」里一起删掉，想看要重新下载。")
            .setNegativeButton("留着", null)
            .setPositiveButton("删除", new android.content.DialogInterface.OnClickListener() {
                @Override public void onClick(android.content.DialogInterface d, int which) { deleteTask(t); }
            })
            .show();
    }

    /** 已完成的下载：把文件从 Downloads 里删干净，列表里那条也去掉 */
    private void deleteTask(final DlTask t) {
        new Thread(new Runnable() { public void run() {
            final Uri u = t.target;
            File f = t.file;
            int n = -1;
            if (u != null) {
                try { n = getContentResolver().delete(u, null, null); } catch (Exception e) { Log.w(TAG, "delete row failed", e); }
            }
            boolean fd = false;
            if (f != null && f.exists()) fd = f.delete();
            Log.i(TAG, "delete task row=" + n + " file=" + fd);
            final boolean askSystem = (u != null && n <= 0 && !fd && Build.VERSION.SDK_INT >= 30);
            tasks.remove(t);
            ui.post(new Runnable() { public void run() {
                rebuildDlRows();
                updateChip();
                if (askSystem) { askSystemDelete(u); return; }
                Toast.makeText(MainActivity.this, "已删除", Toast.LENGTH_SHORT).show();
                refreshOwnedCount();
            }});
        }}).start();
    }

    /** 系统不让删（部分 ROM）：请求系统弹一次删除确认 */
    private void askSystemDelete(Uri u) {
        try {
            android.app.PendingIntent pi = android.provider.MediaStore.createDeleteRequest(
                getContentResolver(), java.util.Collections.singletonList(u));
            startIntentSenderForResult(pi.getIntentSender(), 42, null, 0, 0, 0);
        } catch (Exception e) {
            say("系统不让脚本删这个文件，请去「下载」目录删");
            openDownloadFolder();
        }
    }

    @Override protected void onActivityResult(int req, int res, Intent data) {
        super.onActivityResult(req, res, data);
        if (req == 42 && res == RESULT_OK) {
            Toast.makeText(this, "已删除", Toast.LENGTH_SHORT).show();
        }
    }

    private void fillRowText(final DlTask t) {
        if (t.line == null) return;
        String txt;
        String spd = fmtSpeed(t.speedBps);
        if (t.doneOk) txt = t.name + "\n已完成 · " + fmtMB(t.done) + " → 内部存储/Download";
        else if (t.cancel) txt = t.name + "\n已取消";
        else if (t.failed) txt = t.name + "\n失败：" + t.error;
        else if (!t.running) txt = t.name + "\n排队中（等待双路空位）";
        else if (t.total > 0) txt = t.name + "\n下载中 " + (t.done * 100 / t.total) + "% · " + fmtMB(t.done) + " / " + fmtMB(t.total) + spd;
        else txt = t.name + "\n已下载 " + fmtMB(t.done) + spd;
        final String f = txt;
        ui.post(new Runnable() { public void run() {
            if (t.line != null) t.line.setText(f);
            if (t.bar != null) {
                if (t.doneOk) {
                    t.bar.setVisibility(android.view.View.VISIBLE);
                    t.bar.setIndeterminate(false);
                    t.bar.setProgress(100);
                } else if (t.failed || t.cancel) {
                    t.bar.setVisibility(android.view.View.GONE);
                } else if (!t.running) {
                    t.bar.setVisibility(android.view.View.VISIBLE);
                    t.bar.setIndeterminate(false);
                    t.bar.setProgress(0);
                } else if (t.total > 0) {
                    t.bar.setVisibility(android.view.View.VISIBLE);
                    t.bar.setIndeterminate(false);
                    t.bar.setProgress((int) Math.min(100, t.done * 100 / t.total));
                } else {
                    t.bar.setVisibility(android.view.View.VISIBLE);
                    t.bar.setIndeterminate(true);
                }
            }
        }});
    }

    private void refreshTaskRow(DlTask t) {
        fillRowText(t);
        if (dlPanel != null && dlPanel.getVisibility() == android.view.View.VISIBLE && dlList != null
                && dlList.getChildCount() != tasks.size()) {
            ui.post(new Runnable() { public void run() { rebuildDlRows(); } });
        }
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
        if (customView != null) { exitFullscreen(); return; }
        if (dlPanel != null && dlPanel.getVisibility() == android.view.View.VISIBLE) { closeDlPanel(); return; }
        if (pendingLogin != null || (web != null && !onPlayerPage())) { backToPlayer(); return; }
        if (web != null && onPlayerPage()) {
            web.evaluateJavascript("(function(){return window.__yhktBack?window.__yhktBack():'none';})()",
                new android.webkit.ValueCallback<String>() {
                    @Override public void onReceiveValue(String val) {
                        if (val != null && val.contains("handled")) return;
                        handleNativeBackFallback();
                    }
                });
            return;
        }
        if (web != null && web.canGoBack()) { web.goBack(); return; }
        super.onBackPressed();
    }

    private void handleNativeBackFallback() {
        if (web != null && web.canGoBack()) { web.goBack(); return; }
        long now = System.currentTimeMillis();
        if (now - lastBackPressMs < 2000L) {
            finish();
            return;
        }
        lastBackPressMs = now;
        Toast.makeText(this, "再按一次返回退出延河课堂", Toast.LENGTH_SHORT).show();
    }

    @Override protected void onDestroy() {
        try { dlPool.shutdownNow(); } catch (Exception ignored) {}
        try { if (proc != null) proc.destroy(); } catch (Exception ignored) {}
        if (web != null) { web.loadUrl("about:blank"); web.destroy(); }
        super.onDestroy();
    }
}