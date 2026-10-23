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
    private final java.util.List<String> sessionCookies = new java.util.ArrayList<String>();
    private FrameLayout rootView;
    private android.widget.LinearLayout dlPanel;
    private android.widget.LinearLayout dlList;
    private TextView dlChip;
    private android.widget.ScrollView dlScroll;


    private volatile String lastReqDump = "";
    private volatile String capturedOld = null;
    private volatile String capturedY2 = null;
    private android.view.View customView;
    private WebChromeClient.CustomViewCallback customCb;
    private volatile boolean forceNext = false;

    @Override
    protected void onCreate(Bundle b) {
        super.onCreate(b);
        requestWindowFeature(Window.FEATURE_NO_TITLE);
        CookieManager cm = CookieManager.getInstance();
        cm.setAcceptCookie(true);

        FrameLayout root = new FrameLayout(this);
        rootView = root;
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

        // 下载进度条（含取消按钮）：浮在底栏上方，不占页面空间
        // 右上角「下载 N 个 · 查看」入口；点开是下载面板
        dlChip = new TextView(this);
        dlChip.setText("下载");
        dlChip.setTextSize(12f);
        dlChip.setTextColor(Color.parseColor("#06101f"));
        dlChip.setBackgroundColor(Color.parseColor("#5b9cff"));
        dlChip.setPadding(dp(10), dp(5), dp(10), dp(5));
        dlChip.setVisibility(android.view.View.GONE);
        dlChip.setOnClickListener(new android.view.View.OnClickListener() {
            @Override public void onClick(android.view.View v) { openDlPanel(); }
        });
        FrameLayout.LayoutParams lpChip = new FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lpChip.gravity = Gravity.TOP | Gravity.END;
        lpChip.topMargin = dp(6);
        lpChip.rightMargin = dp(8);
        root.addView(dlChip, lpChip);

        // 下载面板（盖住整页）：列出任务、进度、取消
        dlPanel = new android.widget.LinearLayout(this);
        dlPanel.setOrientation(android.widget.LinearLayout.VERTICAL);
        dlPanel.setBackgroundColor(Color.parseColor("#f20f1115"));
        dlPanel.setPadding(dp(14), dp(14), dp(14), dp(14));
        dlPanel.setVisibility(android.view.View.GONE);
        dlPanel.setClickable(true);

        android.widget.LinearLayout head = new android.widget.LinearLayout(this);
        head.setOrientation(android.widget.LinearLayout.HORIZONTAL);
        head.setGravity(Gravity.CENTER_VERTICAL);
        TextView title = new TextView(this);
        title.setText("下载");
        title.setTextColor(Color.parseColor("#e8eaed"));
        title.setTextSize(16f);
        head.addView(title, new android.widget.LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        android.widget.Button closeBtn = new android.widget.Button(this);
        closeBtn.setText("关闭");
        closeBtn.setTextSize(12f);
        closeBtn.setOnClickListener(new android.view.View.OnClickListener() {
            @Override public void onClick(android.view.View v) { closeDlPanel(); }
        });
        head.addView(closeBtn);
        dlPanel.addView(head, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        TextView tip = new TextView(this);
        tip.setText("下载完成后文件在 内部存储/Download（系统「下载」目录）");
        tip.setTextColor(Color.parseColor("#6f7889"));
        tip.setTextSize(11f);
        tip.setPadding(0, dp(4), 0, dp(8));
        dlPanel.addView(tip);

        dlScroll = new android.widget.ScrollView(this);
        dlList = new android.widget.LinearLayout(this);
        dlList.setOrientation(android.widget.LinearLayout.VERTICAL);
        dlScroll.addView(dlList, new android.widget.FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        dlPanel.addView(dlScroll, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f));

        root.addView(dlPanel, new FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        setContentView(root);
        startEngine();
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
        if (newOk) { openPlayer("/y2"); return; }
        if (oldOk) { openPlayer("/"); return; }
        startLogin();
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

    // ---------------------------------------------------------------- 登录（两步：老平台 → 新版平台）

    private void startLogin() {
        if (loggingIn) return;
        loggingIn = true;
        bar.setVisibility(TextView.VISIBLE);
        bar.setText("请在下方登录延河课堂；老平台、新版平台各登录一次。检测不到就点这条跳过");
        bar.setOnClickListener(new android.view.View.OnClickListener() {
            @Override public void onClick(android.view.View v) {
                forceNext = true;
                bar.setText("跳过这一步…");
            }
        });
        web.loadUrl(OLD_SITE);
        new Thread(new Runnable() { public void run() {
            // 1) 老平台：cookie yht（或 localStorage.auth.token）
            String tok = waitOldToken(120000);
            boolean oldOk = false;
            String oldMsg = "";
            if (tok != null) {
                String res = postLoginCandidates("/api/login", "token", tok);
                oldOk = "OK".equals(res);
                oldMsg = oldOk ? "" : res;
                Log.i(TAG, "old login -> " + res);
                final String t1 = oldOk ? "老平台已登录。接下来登录新版平台（同一个账号再登一次）"
                                        : "老平台登录被引擎拒绝：" + brief(oldMsg) + "。继续试新版平台…";
                ui.post(new Runnable() { public void run() {
                    bar.setText(t1);
                    web.loadUrl(Y2_SITE + "/portal/");
                }});
            }
            // 2) 新版平台：cookie _token / document.cookie
            String ck = waitY2Token(150000);
            boolean newOk = false;
            String newMsg = "";
            if (ck != null) {
                String res2 = postLoginCandidates("/api/y2/login", "jwt", ck);
                newOk = "OK".equals(res2);
                newMsg = newOk ? "" : res2;
                Log.i(TAG, "y2 login -> " + res2);
            }
            final boolean fOld = oldOk, fNew = newOk;
            final String fMsg = (oldMsg + " / " + newMsg).trim();
            loggingIn = false;
            ui.post(new Runnable() { public void run() {
                if (fNew) { openPlayer("/y2"); if (!fOld) ui.postDelayed(offerOldLogin, 4000); return; }
                if (fOld) { openPlayer("/"); return; }
                bar.setVisibility(TextView.VISIBLE);
                bar.setText("两个平台都没登录成功：" + brief(fMsg) + "（点这里重试，屏幕上是诊断信息）");
                bar.setOnClickListener(new android.view.View.OnClickListener() {
                    @Override public void onClick(android.view.View v) { startLogin(); }
                });
                showDiag(fMsg);
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
                h.append("<html><head><meta name='viewport' content='width=device-width,initial-scale=1'>")
                 .append("<style>body{background:#0f1115;color:#e8eaed;font:13px/1.6 -apple-system,system-ui;margin:0;padding:14px}")
                 .append("h3{font-size:13px;margin:14px 0 5px;color:#9aa3b2}")
                 .append("pre{white-space:pre-wrap;word-break:break-all;background:#171a21;border:1px solid #2a2f3a;")
                 .append("border-radius:8px;padding:9px;font:11.5px/1.5 ui-monospace,Consolas,monospace;margin:0}</style></head><body>")
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
    /** 把引擎返回的 JSON 压成一行短提示 */
    private static String brief(String s) {
        if (s == null) return "";
        int i = s.indexOf("\"error\"");
        if (i >= 0) {
            int a = s.indexOf('"', s.indexOf(':', i));
            int b = a >= 0 ? s.indexOf('"', a + 1) : -1;
            if (a >= 0 && b > a) return s.substring(a + 1, b);
        }
        return s.length() > 60 ? s.substring(0, 60) : s;
    }

    private String waitOldToken(int ms) {
        long t0 = System.currentTimeMillis();
        int tick = 0;
        while (System.currentTimeMillis() - t0 < ms) {
            if (forceNext) { forceNext = false; return null; }
            // 0) 优先用从网络请求里截下来的 Authorization（最可信）
            if (capturedOld != null) return capturedOld + "\n" + (jsEval(TOKEN_JS) == null ? "" : jsEval(TOKEN_JS));
            // 1) 某些环境下老平台确实会种 yht cookie
            String ck = CookieManager.getInstance().getCookie(OLD_SITE);
            String tok = cookieValue(ck, "yht");
            if (tok != null && tok.length() > 20) return Uri.decode(tok);
            // 2) 主要还是 localStorage.auth —— 电脑版去读 leveldb 拿的就是这个
            String js = jsEval(TOKEN_JS);
            if (js != null && js.length() > 20) return js;
            if (++tick % 5 == 0) {
                final String t = "老平台：等你登录…（已等 " + ((System.currentTimeMillis() - t0) / 1000) + " 秒）";
                ui.post(new Runnable() { public void run() { if (loggingIn) bar.setText(t); } });
            }
            sleep(1200);
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
            if (++tick % 5 == 0) {
                final String t = "新版平台：等你登录…（已等 " + ((System.currentTimeMillis() - t0) / 1000) + " 秒）";
                ui.post(new Runnable() { public void run() { if (loggingIn) bar.setText(t); } });
            }
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

    private void openPlayer(final String path) {
        loggingIn = false;
        ui.post(new Runnable() { public void run() {
            bar.setVisibility(TextView.GONE);
            // setCookie 是异步落盘的：先把会话 cookie 重新写一遍并读回确认，再导航，
            // 否则页面可能在 cookie 到位之前就请求 /api/status，显示"未登录"
            ensureSessionCookies();
            web.loadUrl(BASE + path);
            ui.removeCallbacks(watch);
            ui.postDelayed(watch, 60000);
            ui.postDelayed(probe, 2500);
            ui.postDelayed(probe, 8000);
        }});
    }

    /** 把登录拿到的 Set-Cookie 重新种一遍，并等它真正可读 */
    private void ensureSessionCookies() {
        for (String pair : sessionCookies) {
            CookieManager.getInstance().setCookie(BASE, pair);
        }
        CookieManager.getInstance().flush();
        for (int i = 0; i < 10; i++) {
            String have = CookieManager.getInstance().getCookie(BASE);
            boolean all = true;
            for (String pair : sessionCookies) {
                String name = pair.split("=", 2)[0];
                if (have == null || !have.contains(name + "=")) { all = false; break; }
            }
            if (all || sessionCookies.isEmpty()) return;
            sleep(100);
        }
    }

    /** 显示本地引擎认为的登录状态，出问题时一眼能看出卡在哪一边 */
    private final Runnable probe = new Runnable() { public void run() {
        new Thread(new Runnable() { public void run() {
            String a = tryHttp(BASE + "/api/status", null);
            String b = tryHttp(BASE + "/api/y2/status", null);
            final boolean ao = a != null && a.indexOf("\"loggedIn\":true") >= 0;
            final boolean bo = b != null && b.indexOf("\"loggedIn\":true") >= 0;
            Log.i(TAG, "probe old=" + ao + " y2=" + bo + " cookies=" + CookieManager.getInstance().getCookie(BASE));
            ui.post(new Runnable() { public void run() {
                if (ao && bo) {
                    // 两边都通了：别留下任何"未登录"的提示
                    ui.removeCallbacks(offerOldLogin);
                    bar.setOnClickListener(null);
                    bar.setVisibility(TextView.GONE);
                    return;
                }
                bar.setVisibility(TextView.VISIBLE);
                bar.setText("本地状态：老平台" + (ao ? "已登录" : "未登录") + " · 新版" + (bo ? "已登录" : "未登录"));
                ui.postDelayed(new Runnable() { public void run() { bar.setVisibility(TextView.GONE); } }, 7000);
            }});
        }}).start();
    }};

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

    private void exitFullscreen() {
        if (customView == null) return;
        try { rootView.removeView(customView); } catch (Exception ignored) {}
        customView = null;
        if (web != null) web.setVisibility(android.view.View.VISIBLE);
        getWindow().clearFlags(android.view.WindowManager.LayoutParams.FLAG_FULLSCREEN);
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

    /** 新版成功了但老平台没成功时，给一个补登录入口（往年课要走老平台） */
    private final Runnable offerOldLogin = new Runnable() { public void run() {
        if (loggingIn) return;
        new Thread(new Runnable() { public void run() {
            String st = tryHttp(BASE + "/api/status", null);
            final boolean ok = st != null && st.indexOf("\"loggedIn\":true") >= 0;
            ui.post(new Runnable() { public void run() {
                if (ok || loggingIn) return;                 // 已经登录了就别再提示
                bar.setVisibility(TextView.VISIBLE);
                bar.setText("老平台未登录（往年课要用）—— 点这里登录");
                bar.setOnClickListener(new android.view.View.OnClickListener() {
                    @Override public void onClick(android.view.View v) {
                        bar.setOnClickListener(null);
                        oldLoginFlow();
                    }
                });
            }});
        }}).start();
    }};

    private void oldLoginFlow() {
        loggingIn = true;
        bar.setVisibility(TextView.VISIBLE);
        bar.setText("正在打开老平台，登录后会自动继续…");
        web.loadUrl(OLD_SITE);
        new Thread(new Runnable() { public void run() {
            String tok = waitOldToken(180000);
            boolean ok = false;
            String msg = "没在页面里找到 token";
            if (tok != null) {
                String res = postLoginCandidates("/api/login", "token", tok);
                ok = "OK".equals(res);
                msg = ok ? "" : res;
                Log.i(TAG, "old login only -> " + res);
            }
            final boolean fOk = ok;
            final String fMsg = msg;
            loggingIn = false;
            ui.post(new Runnable() { public void run() {
                if (fOk) {
                    bar.setText("老平台已登录");
                    ensureSessionCookies();
                    openPlayer("/");
                    ui.postDelayed(new Runnable() { public void run() { bar.setVisibility(TextView.GONE); } }, 2500);
                } else {
                    bar.setText("老平台还是不行：" + brief(fMsg) + "（点这里重试）");
                    bar.setOnClickListener(new android.view.View.OnClickListener() {
                        @Override public void onClick(android.view.View v) { bar.setOnClickListener(null); oldLoginFlow(); }
                    });
                    showDiag(fMsg);
                }
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
                    for (int i = sessionCookies.size() - 1; i >= 0; i--) { if (sessionCookies.get(i).startsWith(cname + "=")) sessionCookies.remove(i); }
                    sessionCookies.add(pair);
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

    /** 一个下载任务：面板里能看到进度，也能取消 */
    static class DlTask {
        String name = "准备中…";
        volatile long done = 0, total = -1;
        volatile boolean cancel = false, doneOk = false, failed = false;
        volatile String error = null;
        volatile HttpURLConnection conn = null;
        volatile Thread th = null;
        TextView line = null;
    }

    private final java.util.List<DlTask> tasks = new java.util.concurrent.CopyOnWriteArrayList<DlTask>();
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
        final DlTask t = new DlTask();
        tasks.add(t);
        lastTask = t;
        updateChip();
        openDlPanel();
        t.th = new Thread(new Runnable() { public void run() { runDownload(url, t); } });
        t.th.start();
    }

    private void runDownload(final String url, final DlTask t) {
        HttpURLConnection c = null;
        Uri target = null;
        OutputStream os = null;
        String name = null;
        try {
            c = (HttpURLConnection) new URL(url).openConnection();
            t.conn = c;
            String cookie = CookieManager.getInstance().getCookie(BASE);
            if (cookie != null) c.setRequestProperty("Cookie", cookie);
            c.setConnectTimeout(6000);
            c.setReadTimeout(60000);
            c.setRequestProperty("Accept-Encoding", "identity");
            int code = c.getResponseCode();
            if (code >= 400) throw new java.io.IOException("HTTP " + code);
            t.total = c.getContentLength();
            name = fileName(c.getHeaderField("content-disposition"));
            if (name == null) name = "lecture_" + System.currentTimeMillis() + ".mp4";
            t.name = name;
            String mime = name.endsWith(".ts") ? "video/mp2t" : "video/mp4";
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
            long lastPush = 0;
            int n;
            while ((n = in.read(buf)) > 0) {
                if (t.cancel) throw new java.io.IOException("已取消");
                os.write(buf, 0, n);
                t.done += n;
                long now = System.currentTimeMillis();
                if (now - lastPush > 400) { lastPush = now; refreshTaskRow(t); }
            }
            if (t.cancel) throw new java.io.IOException("已取消");
            os.flush();
            t.doneOk = true;
        } catch (Exception e) {
            if (t.cancel) t.error = "已取消"; else { t.failed = true; t.error = e.getMessage(); }
            Log.e(TAG, "download failed", e);
        } finally {
            try { if (os != null) os.close(); } catch (Exception ignored) {}
            if (c != null) c.disconnect();
            t.conn = null;
        }
        final String fName = t.name;
        final long total = t.done;
        if (t.doneOk && Build.VERSION.SDK_INT >= 29 && target != null) {
            android.content.ContentValues done = new android.content.ContentValues();
            done.put(android.provider.MediaStore.MediaColumns.IS_PENDING, 0);
            getContentResolver().update(target, done, null, null);
        }
        if ((t.failed || t.cancel) && Build.VERSION.SDK_INT >= 29 && target != null) {
            try { getContentResolver().delete(target, null, null); } catch (Exception ignored) {}
        }
        refreshTaskRow(t);
        updateChip();
        ui.post(new Runnable() { public void run() {
            if (t.doneOk) {
                Toast.makeText(MainActivity.this,
                    "下载完成：" + fName + "\n" + (total / 1048576.0) + " MB → 内部存储/Download",
                    Toast.LENGTH_LONG).show();
            } else if (t.cancel) {
                Toast.makeText(MainActivity.this, "已取消下载", Toast.LENGTH_SHORT).show();
            } else {
                say("下载失败：" + t.error);
            }
        }});
    }

    // ---- 下载面板 UI ----

    /** 页面汉堡栏里可以调这个打开下载面板 */
    @JavascriptInterface
    public void openDownloads() {
        openDlPanel();
    }

    private void openDlPanel() {
        ui.post(new Runnable() { public void run() {
            if (dlPanel == null) return;
            rebuildDlRows();
            dlPanel.setVisibility(android.view.View.VISIBLE);
        }});
    }

    private void closeDlPanel() {
        ui.post(new Runnable() { public void run() {
            if (dlPanel != null) dlPanel.setVisibility(android.view.View.GONE);
        }});
        updateChip();
    }

    private int activeCount() {
        int n = 0;
        for (DlTask t : tasks) if (!t.doneOk && !t.failed && !t.cancel) n++;
        return n;
    }

    private void updateChip() {
        final int n = activeCount();
        ui.post(new Runnable() { public void run() {
            if (dlChip == null) return;
            if (n > 0) { dlChip.setText("下载 " + n + " 个 · 查看"); dlChip.setVisibility(android.view.View.VISIBLE); }
            else dlChip.setVisibility(android.view.View.GONE);
        }});
    }

    private void rebuildDlRows() {
        if (dlList == null) return;
        dlList.removeAllViews();
        if (tasks.isEmpty()) {
            TextView tv = new TextView(this);
            tv.setText("还没有下载任务。在播放页点「下载」后，这里会出现进度。");
            tv.setTextColor(Color.parseColor("#9aa3b2"));
            tv.setTextSize(13f);
            tv.setPadding(dp(4), dp(12), dp(4), dp(12));
            dlList.addView(tv);
            return;
        }
        for (DlTask t : tasks) dlList.addView(rowOf(t));
    }

    private android.view.View rowOf(final DlTask t) {
        android.widget.LinearLayout box = new android.widget.LinearLayout(this);
        box.setOrientation(android.widget.LinearLayout.VERTICAL);
        box.setPadding(0, dp(8), 0, dp(8));

        TextView line = new TextView(this);
        line.setTextColor(Color.parseColor("#e8eaed"));
        line.setTextSize(13f);
        t.line = line;
        box.addView(line, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        android.widget.Button btn = new android.widget.Button(this);
        btn.setText("取消");
        btn.setTextSize(12f);
        btn.setOnClickListener(new android.view.View.OnClickListener() {
            @Override public void onClick(android.view.View v) {
                t.cancel = true;
                HttpURLConnection c = t.conn;
                if (c != null) { try { c.disconnect(); } catch (Exception ignored) {} }
                if (t.line != null) t.line.setText(t.name + " · 正在取消…");
            }
        });
        box.addView(btn, new android.widget.LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        fillRowText(t);
        return box;
    }

    private void fillRowText(final DlTask t) {
        if (t.line == null) return;
        String txt;
        if (t.doneOk) txt = t.name + "\n已完成 · " + (t.done / 1048576.0) + " MB → 内部存储/Download";
        else if (t.cancel) txt = t.name + "\n已取消";
        else if (t.failed) txt = t.name + "\n失败：" + t.error;
        else if (t.total > 0) txt = t.name + "\n" + (t.done * 100 / t.total) + "%（" + (t.done / 1048576.0) + " / " + (t.total / 1048576.0) + " MB）";
        else txt = t.name + "\n已下载 " + (t.done / 1048576.0) + " MB";
        final String f = txt;
        ui.post(new Runnable() { public void run() { if (t.line != null) t.line.setText(f); } });
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
        if (web != null && web.canGoBack()) { web.goBack(); return; }
        super.onBackPressed();
    }

    @Override protected void onDestroy() {
        try { if (proc != null) proc.destroy(); } catch (Exception ignored) {}
        if (web != null) { web.loadUrl("about:blank"); web.destroy(); }
        super.onDestroy();
    }
}
