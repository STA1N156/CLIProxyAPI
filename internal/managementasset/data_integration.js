(function () {
  "use strict";

  var criteriaKeys = [
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
    statsController: null,
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
      var layout = document.getElementById("cpa-di-layout");
      var messageField = document.getElementById("cpa-di-message-field");
      params.set("count", count ? count.value : "1");
      params.set("format", format ? format.value : "json");
      params.set("layout", layout ? layout.value : "raw");
      if (layout && layout.value === "contract") {
        params.set("message_field", messageField ? messageField.value : "messages");
      }
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

  function tokenBillions(value) {
    return (Number(value || 0) / 1000000000).toLocaleString("en-US", {
      maximumFractionDigits: 9,
    }) + " B";
  }

  function ensureStyle() {
    if (document.getElementById("cpa-di-style")) {
      return;
    }
    var style = document.createElement("style");
    style.id = "cpa-di-style";
    style.textContent =
      "#cpa-di-entry{display:flex;align-items:center;gap:10px;width:calc(100% - 16px);margin:8px;padding:11px 12px;border:1px solid rgba(79,70,229,.18);border-radius:10px;background:linear-gradient(135deg,rgba(79,70,229,.14),rgba(14,165,233,.09));color:inherit;font:inherit;cursor:pointer;text-align:left;transition:.18s ease}" +
      "#cpa-di-entry:hover{border-color:rgba(79,70,229,.38);transform:translateY(-1px)}" +
      "#cpa-di-fallback{position:fixed;left:0;top:48%;z-index:2147482000;border:0;border-radius:0 12px 12px 0;padding:13px 10px;background:#4f46e5;color:#fff;box-shadow:0 8px 24px rgba(79,70,229,.3);cursor:pointer;writing-mode:vertical-rl;letter-spacing:2px}" +
      "#cpa-di-overlay{position:fixed;inset:0;z-index:2147483000;background:radial-gradient(circle at 15% 0,rgba(99,102,241,.10),transparent 32%),#f7f8fc;color:#172033;overflow:auto;font-family:Inter,system-ui,-apple-system,\"Segoe UI\",sans-serif}" +
      "#cpa-di-overlay *{box-sizing:border-box}" +
      ".cpa-di-shell{max-width:1180px;margin:0 auto;padding:30px 24px 56px}" +
      ".cpa-di-header{display:flex;align-items:center;justify-content:space-between;gap:20px;margin-bottom:24px}.cpa-di-brand{display:flex;align-items:center;gap:14px}" +
      ".cpa-di-logo{display:grid;place-items:center;width:46px;height:46px;border-radius:14px;background:linear-gradient(135deg,#4f46e5,#0ea5e9);box-shadow:0 10px 26px rgba(79,70,229,.25);color:#fff;font-size:22px}" +
      ".cpa-di-title{margin:0;font-size:28px;line-height:1.2}.cpa-di-sub{margin:6px 0 0;color:#667085}.cpa-di-actions{display:flex;gap:9px}" +
      ".cpa-di-button{border:1px solid #d0d5dd;border-radius:10px;background:#fff;color:#344054;padding:10px 15px;font:inherit;font-weight:600;cursor:pointer;transition:.15s ease}.cpa-di-button:hover{background:#f9fafb}.cpa-di-button:disabled{opacity:.45;cursor:not-allowed}" +
      ".cpa-di-card{background:rgba(255,255,255,.96);border:1px solid #e4e7ec;border-radius:16px;padding:21px;box-shadow:0 8px 28px rgba(16,24,40,.055);margin-bottom:18px}" +
      ".cpa-di-summary{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:13px}.cpa-di-stat{position:relative;overflow:hidden;background:#f8fafc;border:1px solid #eef2f6;border-radius:13px;padding:17px}" +
      ".cpa-di-stat:first-child{background:linear-gradient(135deg,#eef2ff,#f8fafc)}.cpa-di-stat b{display:block;font-size:27px;line-height:1.2;margin-top:7px;overflow-wrap:anywhere}.cpa-di-stat small{display:block;margin-top:5px;color:#667085}.cpa-di-label{color:#667085;font-size:13px}" +
      ".cpa-di-meta{display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;margin-top:15px;padding-top:14px;border-top:1px solid #eef2f6;color:#667085;font-size:13px}.cpa-di-meta strong{color:#344054}" +
      ".cpa-di-section-head{display:flex;align-items:flex-start;justify-content:space-between;gap:14px;margin-bottom:16px}.cpa-di-section-head strong{font-size:17px}.cpa-di-help{margin:5px 0 0;color:#667085;font-size:13px}" +
      ".cpa-di-badge{border-radius:999px;background:#eef2ff;color:#4338ca;padding:6px 10px;font-size:12px;font-weight:700;white-space:nowrap}" +
      ".cpa-di-criteria{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;margin-top:15px}.cpa-di-check{display:flex;gap:11px;align-items:flex-start;border:1px solid #e4e7ec;border-radius:11px;padding:14px;cursor:pointer;transition:.15s ease}.cpa-di-check:hover{border-color:#a5b4fc;background:#fafaff}.cpa-di-check-selected{border-color:#818cf8;background:#f5f7ff}" +
      ".cpa-di-check input{margin-top:3px;accent-color:#4f46e5}.cpa-di-check small{display:block;color:#667085;margin-top:5px}" +
      ".cpa-di-form{display:flex;gap:12px;align-items:end;flex-wrap:wrap}.cpa-di-field{display:flex;flex:1 1 155px;max-width:240px;flex-direction:column;gap:7px;color:#475467;font-size:13px}" +
      ".cpa-di-field input,.cpa-di-field select{width:100%;min-width:0;border:1px solid #d0d5dd;border-radius:10px;background:#fff;padding:10px 11px;font:inherit;color:#172033;outline:none}.cpa-di-field input:focus,.cpa-di-field select:focus{border-color:#818cf8;box-shadow:0 0 0 3px rgba(99,102,241,.12)}" +
      ".cpa-di-primary{border:0;border-radius:10px;background:linear-gradient(135deg,#4f46e5,#6366f1);color:#fff;padding:11px 18px;font:inherit;font-weight:700;cursor:pointer;box-shadow:0 7px 18px rgba(79,70,229,.2)}.cpa-di-primary:disabled{opacity:.45;cursor:not-allowed;box-shadow:none}" +
      ".cpa-di-status{min-height:22px;margin-top:13px;color:#667085;font-size:13px}.cpa-di-error{color:#b42318}.cpa-di-success{color:#067647}" +
      ".cpa-di-danger{display:flex;align-items:center;justify-content:space-between;gap:18px;border-color:#fecdca;background:#fffafa}.cpa-di-danger strong{color:#b42318}.cpa-di-danger p{margin:5px 0 0;color:#7a271a;font-size:13px}.cpa-di-danger-button{border:1px solid #f04438;border-radius:10px;background:#fff;color:#b42318;padding:10px 15px;font:inherit;font-weight:700;cursor:pointer;white-space:nowrap}.cpa-di-danger-button:hover{background:#fff1f0}.cpa-di-danger-button:disabled{opacity:.45;cursor:not-allowed}" +
      "@media(max-width:820px){.cpa-di-summary{grid-template-columns:repeat(2,minmax(0,1fr))}.cpa-di-criteria{grid-template-columns:1fr}}" +
      "@media(max-width:560px){.cpa-di-shell{padding:20px 13px 40px}.cpa-di-header{align-items:flex-start}.cpa-di-sub{display:none}.cpa-di-actions{flex-direction:column}.cpa-di-summary{grid-template-columns:1fr 1fr}.cpa-di-stat b{font-size:21px}.cpa-di-danger{align-items:flex-start;flex-direction:column}.cpa-di-danger-button{width:100%}}";
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
      '<div class="cpa-di-header"><div class="cpa-di-brand"><div class="cpa-di-logo">◆</div><div><h1 class="cpa-di-title">数据整合</h1><p class="cpa-di-sub">筛选、统计并导出符合合同要求的请求数据</p></div></div><div class="cpa-di-actions"><button class="cpa-di-button" id="cpa-di-refresh">刷新</button><button class="cpa-di-button" id="cpa-di-close">返回面板</button></div></div>' +
      '<div class="cpa-di-card"><div class="cpa-di-summary">' +
      '<div class="cpa-di-stat"><span class="cpa-di-label">请求满足比率</span><b id="cpa-di-rate">--</b></div>' +
      '<div class="cpa-di-stat"><span class="cpa-di-label">满足 / 总请求</span><b id="cpa-di-counts">--</b></div>' +
      '<div class="cpa-di-stat"><span class="cpa-di-label">匹配 Token 总数（B）</span><b id="cpa-di-tokens">--</b></div>' +
      '<div class="cpa-di-stat"><span class="cpa-di-label">后台队列</span><b id="cpa-di-queue">--</b><small id="cpa-di-dropped">未发生丢弃</small></div>' +
      '</div><div class="cpa-di-meta"><span>存储位置：<strong id="cpa-di-storage">/data</strong></span><span id="cpa-di-updated">等待读取统计</span></div></div>' +
      '<div class="cpa-di-card"><div class="cpa-di-section-head"><div><strong>筛选条件</strong><p class="cpa-di-help">这里只统计已通过存储硬门槛的数据；勾选条件按“同时满足”计算。</p></div><span class="cpa-di-badge" id="cpa-di-selected-count">已选 4 项</span></div><div class="cpa-di-form">' +
      '<label class="cpa-di-field">开始时间<input id="cpa-di-from" type="datetime-local"></label>' +
      '<label class="cpa-di-field">结束时间<input id="cpa-di-to" type="datetime-local" step="1"></label><button class="cpa-di-button" id="cpa-di-reset-time">全部时间</button></div>' +
      '<div class="cpa-di-criteria" id="cpa-di-criteria"></div><div class="cpa-di-status" id="cpa-di-filter-status"></div></div>' +
      '<div class="cpa-di-card"><div class="cpa-di-section-head"><div><strong>打包下载</strong><p class="cpa-di-help">每条数据单独保存，再统一打包成 ZIP 下载到当前浏览器。</p></div></div><div class="cpa-di-form">' +
      '<label class="cpa-di-field">下载条数<input id="cpa-di-count" type="number" min="1" value="1"></label>' +
      '<label class="cpa-di-field">单条文件格式<select id="cpa-di-format"><option value="json">JSON（每条一个 .json）</option><option value="jsonl">JSONL（每条一个 .jsonl）</option></select></label>' +
      '<label class="cpa-di-field">导出结构<select id="cpa-di-layout"><option value="raw">原始格式</option><option value="contract">合同标准格式</option></select></label>' +
      '<label class="cpa-di-field" id="cpa-di-message-field-wrap" style="display:none">消息序列字段<select id="cpa-di-message-field"><option value="messages">messages</option><option value="conversation">conversation</option><option value="trajectory">trajectory</option></select></label>' +
      '<button class="cpa-di-button" id="cpa-di-all-count">全部条数</button><button class="cpa-di-primary" id="cpa-di-download">下载 ZIP</button></div><div class="cpa-di-status" id="cpa-di-download-status"></div></div>' +
      '<div class="cpa-di-card cpa-di-danger"><div><strong>清理已存数据</strong><p>永久删除数据整合的全部 session 与统计；不会删除凭证或面板配置。</p><div class="cpa-di-status" id="cpa-di-clear-status"></div></div><button class="cpa-di-danger-button" id="cpa-di-clear">清理全部数据</button></div>' +
      "</div>";
    document.body.appendChild(overlay);
    var now = new Date();
    now.setMinutes(now.getMinutes() - now.getTimezoneOffset());
    document.getElementById("cpa-di-to").value = now.toISOString().slice(0, 19);
    document.getElementById("cpa-di-close").onclick = function () {
      if (state.statsController) {
        state.statsController.abort();
      }
      overlay.remove();
    };
    document.getElementById("cpa-di-refresh").onclick = loadStats;
    document.getElementById("cpa-di-from").onchange = loadStats;
    document.getElementById("cpa-di-to").onchange = loadStats;
    document.getElementById("cpa-di-reset-time").onclick = function () {
      document.getElementById("cpa-di-from").value = "";
      document.getElementById("cpa-di-to").value = "";
      loadStats();
    };
    document.getElementById("cpa-di-layout").onchange = function () {
      document.getElementById("cpa-di-message-field-wrap").style.display =
        this.value === "contract" ? "flex" : "none";
    };
    document.getElementById("cpa-di-all-count").onclick = function () {
      if (state.stats) {
        document.getElementById("cpa-di-count").value = String(state.stats.available_download || 0);
      }
    };
    document.getElementById("cpa-di-download").onclick = downloadZIP;
    document.getElementById("cpa-di-clear").onclick = clearData;
    return overlay;
  }

  function renderStats(stats) {
    if (!document.getElementById("cpa-di-overlay")) {
      return;
    }
    state.stats = stats;
    document.getElementById("cpa-di-rate").textContent = percent(stats.match_rate);
    document.getElementById("cpa-di-counts").textContent =
      integer(stats.matched_requests) + " / " + integer(stats.total_requests);
    document.getElementById("cpa-di-tokens").textContent = tokenBillions(stats.matched_tokens);
    document.getElementById("cpa-di-queue").textContent = integer(stats.queue_depth);
    var dropped = Number(stats.dropped_requests || 0);
    document.getElementById("cpa-di-dropped").textContent =
      dropped > 0 ? "已丢弃 " + integer(dropped) + " 条" : "未发生丢弃";
    document.getElementById("cpa-di-storage").textContent = stats.storage_directory || "/data";
    document.getElementById("cpa-di-updated").textContent = stats.updated_at
      ? "统计更新：" + new Date(stats.updated_at).toLocaleString("zh-CN")
      : "统计已更新";
    document.getElementById("cpa-di-selected-count").textContent =
      "已选 " + state.selected.size + " 项";

    var criteriaRoot = document.getElementById("cpa-di-criteria");
    criteriaRoot.innerHTML = "";
    (stats.criteria || []).forEach(function (criterion) {
      var label = document.createElement("label");
      label.className =
        "cpa-di-check" + (state.selected.has(criterion.key) ? " cpa-di-check-selected" : "");
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
    document.getElementById("cpa-di-all-count").disabled = available === 0;
    document.getElementById("cpa-di-clear").disabled = Number(stats.total_requests || 0) === 0;
    document.getElementById("cpa-di-filter-status").textContent =
      "当前满足 " +
      integer(stats.matched_requests) +
      "/" +
      integer(stats.total_requests) +
      " 条，Token 总数 " +
      tokenBillions(stats.matched_tokens);
  }

  async function loadStats() {
    var status = document.getElementById("cpa-di-filter-status");
    if (!status) {
      return;
    }
    if (state.statsController) {
      state.statsController.abort();
    }
    var controller = new AbortController();
    state.statsController = controller;
    var timeout = window.setTimeout(function () {
      controller.abort();
    }, 15000);
    if (status) {
      status.textContent = "正在读取统计…";
      status.className = "cpa-di-status";
    }
    try {
      var response = await fetch(state.base + "/data-integration/stats?" + queryString(false), {
        headers: requestHeaders(),
        cache: "no-store",
        signal: controller.signal,
      });
      if (!response.ok) {
        throw new Error(response.status === 401 ? "请先在管理面板完成连接，再打开数据整合。" : await response.text());
      }
      renderStats(await response.json());
    } catch (error) {
      if (status && state.statsController === controller) {
        status.textContent =
          error && error.name === "AbortError"
            ? "统计读取超过 15 秒，请点击“刷新”重试。"
            : error && error.message
              ? error.message
              : "读取统计失败";
        status.className = "cpa-di-status cpa-di-error";
      }
    } finally {
      window.clearTimeout(timeout);
      if (state.statsController === controller) {
        state.statsController = null;
      }
    }
  }

  async function clearData() {
    if (
      !window.confirm(
        "确定永久清理全部数据整合记录吗？\n\n此操作会删除已存 session 和统计，且无法恢复。"
      )
    ) {
      return;
    }
    var button = document.getElementById("cpa-di-clear");
    var status = document.getElementById("cpa-di-clear-status");
    button.disabled = true;
    status.textContent = "正在清理…";
    status.className = "cpa-di-status";
    try {
      var headers = requestHeaders();
      headers["Content-Type"] = "application/json";
      var response = await fetch(state.base + "/data-integration", {
        method: "DELETE",
        headers: headers,
        body: JSON.stringify({ confirm: "CLEAR_ALL_DATA" }),
        cache: "no-store",
      });
      if (!response.ok) {
        throw new Error(await response.text());
      }
      var result = await response.json();
      status.textContent = "已清理 " + integer(result.removed_requests) + " 条数据。";
      status.className = "cpa-di-status cpa-di-success";
      await loadStats();
    } catch (error) {
      status.textContent = error && error.message ? error.message : "清理失败";
      status.className = "cpa-di-status cpa-di-error";
      button.disabled = false;
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
