(function () {
  "use strict";

  var criteriaKeys = [
    "effective_turns",
    "first_role",
    "tool_call",
    "tool_schema",
    "tool_pairing",
    "machine_ratio",
  ];
  var state = {
    base: window.location.origin + "/v0/management",
    authName: "",
    authValue: "",
    selected: new Set(criteriaKeys),
    stats: null,
  };

  function headerValue(headers, name) {
    try {
      return new Headers(headers || {}).get(name) || "";
    } catch (_) {
      return "";
    }
  }

  function capture(url, headers) {
    var resolved;
    try {
      resolved = new URL(String(url || ""), window.location.href);
    } catch (_) {
      return;
    }
    var marker = "/v0/management";
    var index = resolved.pathname.indexOf(marker);
    if (index < 0) {
      return;
    }
    state.base = resolved.origin + resolved.pathname.slice(0, index) + marker;
    var authorization = headerValue(headers, "Authorization");
    var managementKey = headerValue(headers, "X-Management-Key");
    if (authorization) {
      state.authName = "Authorization";
      state.authValue = authorization;
    } else if (managementKey) {
      state.authName = "X-Management-Key";
      state.authValue = managementKey;
    }
  }

  var originalFetch = window.fetch;
  if (originalFetch) {
    window.fetch = function (input, init) {
      var url = typeof input === "string" || input instanceof URL ? input : input.url;
      capture(url, input && input.headers);
      capture(url, init && init.headers);
      return originalFetch.apply(this, arguments);
    };
  }

  var originalOpen = XMLHttpRequest.prototype.open;
  var originalSetRequestHeader = XMLHttpRequest.prototype.setRequestHeader;
  var originalSend = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.open = function (method, url) {
    this.__cpaDataIntegrationURL = url;
    this.__cpaDataIntegrationHeaders = {};
    return originalOpen.apply(this, arguments);
  };
  XMLHttpRequest.prototype.setRequestHeader = function (name, value) {
    if (this.__cpaDataIntegrationHeaders) {
      this.__cpaDataIntegrationHeaders[name] = value;
    }
    return originalSetRequestHeader.apply(this, arguments);
  };
  XMLHttpRequest.prototype.send = function () {
    capture(this.__cpaDataIntegrationURL, this.__cpaDataIntegrationHeaders);
    return originalSend.apply(this, arguments);
  };

  function requestHeaders() {
    var headers = {};
    if (state.authName && state.authValue) {
      headers[state.authName] = state.authValue;
    }
    return headers;
  }

  function queryString(includeDownloadOptions) {
    var params = new URLSearchParams();
    if (state.selected.size) {
      params.set("criteria", Array.from(state.selected).join(","));
    }
    var from = document.getElementById("cpa-di-from");
    var to = document.getElementById("cpa-di-to");
    if (from && from.value) {
      params.set("from", new Date(from.value).toISOString());
    }
    if (to && to.value) {
      var end = new Date(to.value);
      if (to.value.length === 16) {
        end.setSeconds(59, 999);
      }
      params.set("to", end.toISOString());
    }
    if (includeDownloadOptions) {
      var count = document.getElementById("cpa-di-count");
      var format = document.getElementById("cpa-di-format");
      params.set("count", count ? count.value : "1");
      params.set("format", format ? format.value : "json");
    }
    return params.toString();
  }

  function percent(value) {
    var number = Number(value || 0);
    return number.toFixed(number >= 10 ? 1 : 2) + "%";
  }

  function integer(value) {
    return Number(value || 0).toLocaleString("zh-CN");
  }

  function ensureStyle() {
    if (document.getElementById("cpa-di-style")) {
      return;
    }
    var style = document.createElement("style");
    style.id = "cpa-di-style";
    style.textContent =
      "#cpa-di-entry{display:flex;align-items:center;gap:10px;width:calc(100% - 16px);margin:8px;padding:10px 12px;border:0;border-radius:9px;background:rgba(37,99,235,.12);color:inherit;font:inherit;cursor:pointer;text-align:left}" +
      "#cpa-di-entry:hover{background:rgba(37,99,235,.22)}" +
      "#cpa-di-fallback{position:fixed;left:0;top:48%;z-index:2147482000;border:0;border-radius:0 10px 10px 0;padding:12px 10px;background:#2563eb;color:#fff;cursor:pointer;writing-mode:vertical-rl;letter-spacing:2px}" +
      "#cpa-di-overlay{position:fixed;inset:0;z-index:2147483000;background:#f5f7fb;color:#172033;overflow:auto;font-family:Inter,system-ui,-apple-system,\"Segoe UI\",sans-serif}" +
      "#cpa-di-overlay *{box-sizing:border-box}" +
      ".cpa-di-shell{max-width:1120px;margin:0 auto;padding:28px 24px 48px}" +
      ".cpa-di-header{display:flex;align-items:flex-start;justify-content:space-between;gap:20px;margin-bottom:22px}" +
      ".cpa-di-title{margin:0;font-size:28px}.cpa-di-sub{margin:8px 0 0;color:#667085}" +
      ".cpa-di-close{border:1px solid #d0d5dd;border-radius:9px;background:#fff;padding:9px 15px;cursor:pointer}" +
      ".cpa-di-card{background:#fff;border:1px solid #e4e7ec;border-radius:14px;padding:20px;box-shadow:0 5px 18px rgba(16,24,40,.05);margin-bottom:18px}" +
      ".cpa-di-summary{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px}" +
      ".cpa-di-stat{background:#f8fafc;border-radius:11px;padding:16px}.cpa-di-stat b{display:block;font-size:27px;margin-top:5px}.cpa-di-label{color:#667085;font-size:13px}" +
      ".cpa-di-criteria{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;margin-top:14px}" +
      ".cpa-di-check{display:flex;gap:10px;align-items:flex-start;border:1px solid #e4e7ec;border-radius:10px;padding:13px;cursor:pointer}" +
      ".cpa-di-check input{margin-top:3px}.cpa-di-check small{display:block;color:#667085;margin-top:5px}" +
      ".cpa-di-download{display:flex;gap:12px;align-items:end;flex-wrap:wrap}.cpa-di-field{display:flex;flex-direction:column;gap:6px;color:#475467;font-size:13px}" +
      ".cpa-di-field input,.cpa-di-field select{min-width:150px;border:1px solid #d0d5dd;border-radius:9px;background:#fff;padding:10px 12px;font:inherit;color:#172033}" +
      ".cpa-di-primary{border:0;border-radius:9px;background:#2563eb;color:#fff;padding:11px 18px;font-weight:600;cursor:pointer}.cpa-di-primary:disabled{opacity:.5;cursor:not-allowed}" +
      ".cpa-di-status{min-height:22px;margin-top:12px;color:#667085}.cpa-di-error{color:#b42318}" +
      "@media(max-width:720px){.cpa-di-summary,.cpa-di-criteria{grid-template-columns:1fr}.cpa-di-shell{padding:20px 14px}}";
    document.head.appendChild(style);
  }

  function createOverlay() {
    var existing = document.getElementById("cpa-di-overlay");
    if (existing) {
      return existing;
    }
    ensureStyle();
    var overlay = document.createElement("div");
    overlay.id = "cpa-di-overlay";
    overlay.innerHTML =
      '<div class="cpa-di-shell">' +
      '<div class="cpa-di-header"><div><h1 class="cpa-di-title">数据整合</h1><p class="cpa-di-sub">按勾选条件查看请求满足率，并把最新匹配数据打包下载。</p></div><button class="cpa-di-close" id="cpa-di-close">返回管理面板</button></div>' +
      '<div class="cpa-di-card"><div class="cpa-di-summary">' +
      '<div class="cpa-di-stat"><span class="cpa-di-label">请求满足比率</span><b id="cpa-di-rate">--</b></div>' +
      '<div class="cpa-di-stat"><span class="cpa-di-label">满足 / 总请求</span><b id="cpa-di-counts">--</b></div>' +
      '<div class="cpa-di-stat"><span class="cpa-di-label">存储位置</span><b id="cpa-di-storage" style="font-size:18px">/data</b></div>' +
      "</div></div>" +
      '<div class="cpa-di-card"><strong>筛选条件</strong><div class="cpa-di-download" style="margin-top:14px">' +
      '<label class="cpa-di-field">开始时间<input id="cpa-di-from" type="datetime-local"></label>' +
      '<label class="cpa-di-field">结束时间<input id="cpa-di-to" type="datetime-local"></label></div>' +
      '<div class="cpa-di-criteria" id="cpa-di-criteria"></div><div class="cpa-di-status" id="cpa-di-filter-status"></div></div>' +
      '<div class="cpa-di-card"><strong>打包下载</strong><div class="cpa-di-download" style="margin-top:14px">' +
      '<label class="cpa-di-field">下载条数<input id="cpa-di-count" type="number" min="1" value="1"></label>' +
      '<label class="cpa-di-field">单条文件格式<select id="cpa-di-format"><option value="json">JSON（每条一个 .json）</option><option value="jsonl">JSONL（每条一个 .jsonl）</option></select></label>' +
      '<button class="cpa-di-primary" id="cpa-di-download">下载 ZIP</button></div><div class="cpa-di-status" id="cpa-di-download-status"></div></div>' +
      "</div>";
    document.body.appendChild(overlay);
    document.getElementById("cpa-di-close").onclick = function () {
      overlay.remove();
    };
    document.getElementById("cpa-di-from").onchange = loadStats;
    document.getElementById("cpa-di-to").onchange = loadStats;
    document.getElementById("cpa-di-download").onclick = downloadZIP;
    return overlay;
  }

  function renderStats(stats) {
    state.stats = stats;
    document.getElementById("cpa-di-rate").textContent = percent(stats.match_rate);
    document.getElementById("cpa-di-counts").textContent =
      String(stats.matched_requests) + " / " + String(stats.total_requests);
    document.getElementById("cpa-di-storage").textContent = stats.storage_directory || "/data";

    var criteriaRoot = document.getElementById("cpa-di-criteria");
    criteriaRoot.innerHTML = "";
    (stats.criteria || []).forEach(function (criterion) {
      var label = document.createElement("label");
      label.className = "cpa-di-check";
      var checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.checked = state.selected.has(criterion.key);
      checkbox.onchange = function () {
        if (checkbox.checked) {
          state.selected.add(criterion.key);
        } else {
          state.selected.delete(criterion.key);
        }
        loadStats();
      };
      var text = document.createElement("span");
      text.textContent = criterion.label;
      var detail = document.createElement("small");
      detail.textContent =
        "单项通过 " + String(criterion.matched) + " 条（" + percent(criterion.rate) + "）";
      text.appendChild(detail);
      label.appendChild(checkbox);
      label.appendChild(text);
      criteriaRoot.appendChild(label);
    });

    var count = document.getElementById("cpa-di-count");
    var available = Number(stats.available_download || 0);
    count.max = String(available);
    count.disabled = available === 0;
    if (available > 0) {
      var current = Number(count.value || 1);
      count.value = String(Math.min(Math.max(current, 1), available));
    } else {
      count.value = "0";
    }
    document.getElementById("cpa-di-download").disabled = available === 0;
    document.getElementById("cpa-di-filter-status").textContent =
      "当前满足 " +
      integer(stats.matched_requests) +
      "/" +
      integer(stats.total_requests) +
      " 条，Token 总数 " +
      integer(stats.matched_tokens);
  }

  async function loadStats() {
    var status = document.getElementById("cpa-di-filter-status");
    if (status) {
      status.textContent = "正在读取统计…";
      status.className = "cpa-di-status";
    }
    try {
      var response = await fetch(state.base + "/data-integration/stats?" + queryString(false), {
        headers: requestHeaders(),
        cache: "no-store",
      });
      if (!response.ok) {
        throw new Error(response.status === 401 ? "请先在管理面板完成连接，再打开数据整合。" : await response.text());
      }
      renderStats(await response.json());
    } catch (error) {
      if (status) {
        status.textContent = error && error.message ? error.message : "读取统计失败";
        status.className = "cpa-di-status cpa-di-error";
      }
    }
  }

  async function downloadZIP() {
    var button = document.getElementById("cpa-di-download");
    var status = document.getElementById("cpa-di-download-status");
    button.disabled = true;
    status.textContent = "正在生成 ZIP…";
    status.className = "cpa-di-status";
    try {
      var response = await fetch(state.base + "/data-integration/download?" + queryString(true), {
        headers: requestHeaders(),
        cache: "no-store",
      });
      if (!response.ok) {
        throw new Error(await response.text());
      }
      var blob = await response.blob();
      var disposition = response.headers.get("Content-Disposition") || "";
      var match = disposition.match(/filename="?([^";]+)"?/i);
      var name = match ? match[1] : "data-integration.zip";
      var url = URL.createObjectURL(blob);
      var link = document.createElement("a");
      link.href = url;
      link.download = name;
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(url);
      status.textContent = "下载已开始。";
    } catch (error) {
      status.textContent = error && error.message ? error.message : "下载失败";
      status.className = "cpa-di-status cpa-di-error";
    } finally {
      button.disabled = !state.stats || Number(state.stats.available_download || 0) === 0;
    }
  }

  function openPanel() {
    createOverlay();
    loadStats();
  }

  function findSidebar() {
    var candidates = Array.from(
      document.querySelectorAll('aside,nav,[class*="sidebar" i],[class*="sider" i]')
    ).filter(function (element) {
      if (element.closest("#cpa-di-overlay")) {
        return false;
      }
      var box = element.getBoundingClientRect();
      return box.height > 180 && box.width > 80 && box.width < 420;
    });
    candidates.sort(function (left, right) {
      return right.getBoundingClientRect().height - left.getBoundingClientRect().height;
    });
    return candidates[0] || null;
  }

  function ensureEntry() {
    if (!document.body) {
      return;
    }
    ensureStyle();
    var existing = document.getElementById("cpa-di-entry");
    if (existing && existing.isConnected) {
      return;
    }
    var sidebar = findSidebar();
    if (!sidebar) {
      if (!document.getElementById("cpa-di-fallback")) {
        var fallback = document.createElement("button");
        fallback.id = "cpa-di-fallback";
        fallback.textContent = "数据整合";
        fallback.onclick = openPanel;
        document.body.appendChild(fallback);
      }
      return;
    }
    var fallbackExisting = document.getElementById("cpa-di-fallback");
    if (fallbackExisting) {
      fallbackExisting.remove();
    }
    var button = document.createElement("button");
    button.id = "cpa-di-entry";
    button.innerHTML = "<span>▣</span><span>数据整合</span>";
    button.onclick = openPanel;
    sidebar.appendChild(button);
  }

  function start() {
    ensureEntry();
    var observer = new MutationObserver(function () {
      ensureEntry();
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start, { once: true });
  } else {
    start();
  }
})();
