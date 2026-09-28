// TDM GUI frontend logic.
// Uses the Wails v3 runtime (loaded as /wails/runtime.js) to call bound
// service methods on github.com/NamanBalaji/tdm/gui.App by name.

import { Call } from "/wails/runtime.js";

const APP = "github.com/NamanBalaji/tdm/gui.App.";

function backend(method, ...args) {
    return Call.ByName(APP + method, ...args);
}

let downloads = [];
let currentFilter = "all";
let toastTimer = null;

const statusLabels = {
    active: "● 进行中",
    paused: "❚❚ 已暂停",
    queued: "○ 排队中",
    pending: "○ 等待中",
    initializing: "◌ 准备中",
    completed: "✔ 已完成",
    failed: "✖ 失败",
    cancelled: "⊘ 已取消",
};

const filterGroups = {
    all: null,
    active: ["active", "queued", "pending", "paused", "initializing"],
    completed: ["completed"],
    failed: ["failed", "cancelled"],
};

const filterTitles = {
    all: "概览 · 全部任务",
    active: "进行中的任务",
    completed: "已完成的任务",
    failed: "失败的任务",
};

// ─── 工具函数 ────────────────────────────────────────────

function formatSize(bytes) {
    if (bytes < 1024) return `${bytes} B`;
    const units = ["KiB", "MiB", "GiB", "TiB"];
    let v = bytes, i = -1;
    do { v /= 1024; i++; } while (v >= 1024 && i < units.length - 1);
    return `${v.toFixed(1)} ${units[i]}`;
}

function formatSpeed(bps) {
    return bps > 0 ? `${formatSize(bps)}/s` : "--/s";
}

function formatETA(seconds) {
    if (!seconds || seconds <= 0) return "--";
    if (seconds < 60) return `${Math.round(seconds)}s`;
    const m = Math.floor(seconds / 60);
    const s = Math.round(seconds % 60);
    return `${m}m${s}s`;
}

function escapeHTML(s) {
    return s.replace(/[&<>"']/g, (c) => ({
        "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    }[c]));
}

// ─── 渲染 ────────────────────────────────────────────────

// 卡片 DOM 缓存：id -> 元素。轮询时复用已有卡片只更新数据，避免整表
// 重建 innerHTML 导致的闪烁。
const cardEls = new Map();

function render() {
    const list = document.getElementById("list");
    const filtered = filterGroups[currentFilter]
        ? downloads.filter((d) => filterGroups[currentFilter].includes(d.status))
        : downloads;

    document.getElementById("view-title").textContent = filterTitles[currentFilter];

    if (filtered.length === 0) {
        list.innerHTML = emptyStateHTML();
        cardEls.clear();
        return;
    }

    // 移除空状态占位（首次从空列表变为有任务时）
    list.querySelector(".empty-state")?.remove();

    const seen = new Set();
    let prev = null;
    for (const d of filtered) {
        const id = String(d.id);
        seen.add(id);

        let card = cardEls.get(id);
        if (!card) {
            card = buildCard(d);
            cardEls.set(id, card);
        } else {
            // 状态切换原地更新（徽章/按钮可用性/配色），不重建 DOM，
            // 避免按钮消失出现导致的布局跳动
            updateCard(card, d);
        }

        // 维持后端返回的排序（优先级降序）
        if (prev) {
            if (prev.nextElementSibling !== card) prev.after(card);
        } else if (list.firstElementChild !== card) {
            list.prepend(card);
        }
        prev = card;
    }

    // 移除已消失（被删除或被当前过滤器过滤掉）的卡片
    for (const [id, el] of cardEls) {
        if (!seen.has(id)) {
            el.remove();
            cardEls.delete(id);
        }
    }
}

function emptyStateHTML() {
    return `
        <div class="empty-state">
            <div class="empty-logo">TDM</div>
            <p class="empty-title">暂无下载任务</p>
            <p class="empty-hint">在上方粘贴链接，点击「开始下载」开始你的第一个任务</p>
        </div>`;
}

// 状态 → 该状态下显示的按钮（按需显示，不再四个全渲染置灰）。
// failed 用户未指定，配"继续 + 删除"以保留重试入口。
const CARD_BUTTONS = {
    active:       [{ act: "pause",  label: "暂停", cls: "pause" }],
    queued:       [{ act: "pause",  label: "暂停", cls: "pause" }],
    pending:      [{ act: "pause",  label: "暂停", cls: "pause" }],
    initializing: [{ act: "pause",  label: "暂停", cls: "pause" }],
    paused:       [{ act: "resume", label: "继续", cls: "resume" }, { act: "cancel", label: "取消", cls: "cancel" }],
    failed:       [{ act: "resume", label: "继续", cls: "resume" }, { act: "remove", label: "删除", cls: "remove" }],
    cancelled:    [{ act: "remove", label: "删除", cls: "remove" }],
    completed:    [{ act: "open",   label: "打开目录", cls: "open" }, { act: "remove", label: "删除", cls: "remove" }],
};

const FOLDER_SVG = `<svg viewBox="0 0 16 16" width="12" height="12"><path d="M2 3.5A1.5 1.5 0 0 1 3.5 2h3l1.5 2h4.5A1.5 1.5 0 0 1 14 5.5v7a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 2 12.5z" fill="none" stroke="currentColor" stroke-width="1.3"/></svg>`;


// 完成态卡片：不显示进度条，改为"对勾 + 文件名 + 大小"一行、
// "保存目录 + 打开/删除"一行的归档式收尾布局。
// 本地文件被删除时整体置灰 + 删除线，弱化为"已失效记录"：
// 不用警告色打扰，仅"打开目录"按钮隐藏（目录里已没有该文件），保留删除。
function completedCardInner(d) {
    const missing = !!d.fileMissing;
    const checkSVG = `<svg viewBox="0 0 16 16" width="15" height="15"><circle cx="8" cy="8" r="6.4" fill="none" stroke="currentColor" stroke-width="1.4"/><path d="M5.2 8.3l1.9 1.9 3.7-4.2" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>`;

    return `
        <div class="card-row card-row-top">
            <span class="done-check">${checkSVG}</span>
            <span class="card-title" title="${escapeHTML(d.savePath || d.filename)}">${escapeHTML(d.filename)}</span>
            <span class="card-size">${formatSize(d.downloaded)} / ${formatSize(d.totalSize)}</span>
            <button class="prio-badge" data-act="priority" title="优先级：数字越大越先开始下载，点击修改">P${d.priority}</button>
        </div>
        <div class="card-row done-row">
            ${d.savePath ? `<div class="card-path" title="${escapeHTML(d.savePath)}">${FOLDER_SVG}<span>${escapeHTML(d.savePath)}</span></div>` : "<div class='card-path'></div>"}
            <div class="card-actions">
                ${missing ? "" : `<button class="btn btn-sm btn-open" data-act="open">打开目录</button>`}
                <button class="btn btn-sm btn-remove" data-act="remove">删除</button>
            </div>
        </div>`;
}

// 进行中/其他状态的卡片：进度条布局
function progressCardInner(d, pct) {
    const badge = `<span class="badge badge-${d.status}">${statusLabels[d.status] || d.status}</span>`;

    const actions = (CARD_BUTTONS[d.status] || [])
        .map((b) => `<button class="btn btn-sm btn-${b.cls}" data-act="${b.act}">${b.label}</button>`)
        .join("");

    return `
        <div class="card-row card-row-top">
            <span class="status-dot"></span>
            <span class="card-title" title="${escapeHTML(d.savePath || d.filename)}">${escapeHTML(d.filename)}</span>
            <span class="card-size">${formatSize(d.downloaded)} / ${formatSize(d.totalSize)}</span>
            <button class="prio-badge" data-act="priority" title="优先级：数字越大越先开始下载，点击修改">P${d.priority}</button>
            ${badge}
        </div>
        <div class="progress-row">
            <div class="progress-track">
                <div class="progress-fill st-${d.status}" style="width: ${pct}%"></div>
            </div>
            <span class="card-percent">${pct}%</span>
            <span class="card-speed"></span>
            <div class="card-actions">${actions}</div>
        </div>`;
}

function cardHTML(d) {
    // 百分比去掉无意义的 .0（100.0% → 100%，45.0% → 45%）
    const pct = Math.min(Math.max(d.percentage || 0, 0), 100).toFixed(1).replace(/\.0$/, "");

    // 文件丢失的完成任务追加 file-missing 类（整卡置灰 + 删除线）
    const missingCls = d.status === "completed" && d.fileMissing ? " file-missing" : "";

    return `<div class="card st-${d.status}${missingCls}" data-id="${d.id}">
        ${d.status === "completed" ? completedCardInner(d) : progressCardInner(d, pct)}
    </div>`;
}

function buildCard(d) {
    const tpl = document.createElement("template");
    tpl.innerHTML = cardHTML(d).trim();
    const card = tpl.content.firstElementChild;
    card.dataset.status = d.status;
    bindCardActions(card);
    cacheCardRefs(card);
    return card;
}

// buildCard 时缓存动态字段的元素引用，避免每次轮询 querySelector。
// completed 卡片结构不同，引用在 rebuildCard 后重新收集。
function cacheCardRefs(card) {
    card._refs = {
        pct: card.querySelector(".card-percent"),
        fill: card.querySelector(".progress-fill"),
        speed: card.querySelector(".card-speed"),
        size: card.querySelector(".card-size"),
        prio: card.querySelector(".prio-badge"),
    };
}

// updateCard 只刷新文本/样式等动态字段。布局随状态变化：完成态与进度态
// 结构不同，直接整卡重建（仅状态切换的那一次触发，开销可忽略）。
function updateCard(card, d) {
    if (card.dataset.status !== d.status) {
        rebuildCard(card, d);
        return;
    }

    const refs = card._refs;
    if (!refs || !refs.pct) return;

    // 百分比去掉无意义的 .0
    const pct = Math.min(Math.max(d.percentage || 0, 0), 100).toFixed(1).replace(/\.0$/, "");

    refs.pct.textContent = `${pct}%`;
    refs.fill.style.width = `${pct}%`;
    refs.prio.textContent = `P${d.priority}`;
    refs.size.textContent = `${formatSize(d.downloaded)} / ${formatSize(d.totalSize)}`;
    if (refs.speed) refs.speed.textContent = d.status === "active" ? formatSpeed(d.speedBPS) : "";
}

// 状态切换时整体重建卡片内部结构（保留 DOM 节点本身与事件绑定）。
function rebuildCard(card, d) {
    const pct = Math.min(Math.max(d.percentage || 0, 0), 100).toFixed(1).replace(/\.0$/, "");

    card.classList.remove(`st-${card.dataset.status}`, "file-missing");
    card.classList.add(`st-${d.status}`);
    if (d.status === "completed" && d.fileMissing) card.classList.add("file-missing");
    card.dataset.status = d.status;
    card.innerHTML = d.status === "completed" ? completedCardInner(d) : progressCardInner(d, pct);
    bindCardActions(card);
    cacheCardRefs(card);
}

function bindCardActions(root) {
    root.querySelectorAll("button[data-act]").forEach((btn) => {
        btn.addEventListener("click", async (e) => {
            const id = e.target.closest(".card").dataset.id;
            const act = btn.dataset.act;

            if (act === "priority") {
                openPriorityModal(id);
                return;
            }

            const actMap = {
                pause: "PauseDownload", resume: "ResumeDownload",
                cancel: "CancelDownload", remove: "RemoveDownload",
                open: "RevealSavePath", // 在系统文件管理器中打开并选中文件
            };

            try {
                await backend(actMap[act], id);
            } catch (err) {
                showToast(String(err), "error");
            }
        });
    });
}

// ─── 优先级修改弹窗 ──────────────────────────────────────

const prioModal = document.getElementById("priority-modal");
const prioGrid = document.getElementById("priority-grid");
let prioTargetId = null;

function openPriorityModal(id) {
    const dl = downloads.find((d) => String(d.id) === id);
    if (!dl) return;

    prioTargetId = id;
    document.getElementById("priority-name").textContent = dl.filename || "下载任务";

    // 生成 1-10 选项，高亮当前值
    prioGrid.innerHTML = Array.from({ length: 10 }, (_, i) => {
        const v = i + 1;
        return `<button class="prio-opt ${v === dl.priority ? "current" : ""}" data-value="${v}">${v}</button>`;
    }).join("");

    prioModal.classList.remove("hidden");
}

function closePriorityModal() {
    prioModal.classList.add("hidden");
    prioTargetId = null;
}

prioGrid.addEventListener("click", async (e) => {
    const opt = e.target.closest(".prio-opt");
    if (!opt || !prioTargetId) return;

    try {
        await backend("SetPriority", prioTargetId, parseInt(opt.dataset.value, 10));
        showToast("优先级已修改");
        closePriorityModal();
    } catch (err) {
        showToast(String(err), "error");
    }
});

document.getElementById("priority-cancel").addEventListener("click", closePriorityModal);
prioModal.addEventListener("click", (e) => {
    if (e.target === prioModal) closePriorityModal(); // 点遮罩关闭
});

// ─── 卡片右键菜单 ────────────────────────────────────────

const ctxMenu = document.getElementById("context-menu");
let ctxTargetId = null;

// 在列表容器上委托监听右键事件（卡片是动态增删的）
document.getElementById("list").addEventListener("contextmenu", (e) => {
    const card = e.target.closest(".card");
    if (!card) return;

    e.preventDefault();
    ctxTargetId = card.dataset.id;

    // 显示菜单并限制在窗口范围内
    ctxMenu.classList.remove("hidden");
    const rect = ctxMenu.getBoundingClientRect();
    const x = Math.min(e.clientX, window.innerWidth - rect.width - 6);
    const y = Math.min(e.clientY, window.innerHeight - rect.height - 6);
    ctxMenu.style.left = `${x}px`;
    ctxMenu.style.top = `${y}px`;
});

// 点击其他位置关闭菜单
window.addEventListener("click", () => ctxMenu.classList.add("hidden"));
window.addEventListener("blur", () => ctxMenu.classList.add("hidden"));

ctxMenu.addEventListener("click", async (e) => {
    const item = e.target.closest(".context-item");
    if (!item || !ctxTargetId) return;

    ctxMenu.classList.add("hidden");

    if (item.dataset.ctx === "copy") {
        try {
            await backend("CopyDownloadLink", ctxTargetId);
            showToast("链接已复制到剪贴板");
        } catch (err) {
            showToast(String(err), "error");
        }
    } else if (item.dataset.ctx === "reveal") {
        try {
            await backend("RevealSavePath", ctxTargetId);
        } catch (err) {
            showToast(String(err), "error");
        }
    }
});

// 右键菜单期间禁用原生菜单；输入框保留原生右键（可粘贴）
document.addEventListener("contextmenu", (e) => {
    if (!e.target.closest("#url-input")) e.preventDefault();
});

function renderStats(stats) {
    document.getElementById("global-speed").textContent = `↓ ${formatSpeed(stats.totalBPS)}`;

    // 侧边栏计数徽章
    document.getElementById("nav-count-all").textContent = stats.total;
    document.getElementById("nav-count-active").textContent = stats.active + stats.queued + stats.paused;
    document.getElementById("nav-count-completed").textContent = stats.completed;
    document.getElementById("nav-count-failed").textContent = stats.failed + stats.cancelled;
}

// ─── 通知 ────────────────────────────────────────────────

function showToast(msg, type = "success") {
    const toast = document.getElementById("toast");
    toast.textContent = msg;
    toast.className = `toast ${type}`;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toast.classList.add("hidden"), 3000);
}

// ─── 主题切换 ────────────────────────────────────────────

function applyTheme(theme) {
    document.body.classList.toggle("theme-light", theme === "light");
    document.body.classList.toggle("theme-dark", theme !== "light");
    document.getElementById("theme-label").textContent =
        theme === "light" ? "暗色模式" : "亮色模式";
    try { localStorage.setItem("tdm-theme", theme); } catch (e) { /* ignore */ }
}

function initTheme() {
    let saved = null;
    try { saved = localStorage.getItem("tdm-theme"); } catch (e) { /* ignore */ }
    // 默认亮色模式；用户手动切换后记住选择
    applyTheme(saved === "dark" ? "dark" : "light");

    document.getElementById("nav-theme").addEventListener("click", () => {
        const isLight = document.body.classList.contains("theme-light");
        applyTheme(isLight ? "dark" : "light");
    });
}

// ─── 窗口控制按钮 ────────────────────────────────────────

function initWindowControls() {
    document.getElementById("wc-min").addEventListener("click", () => backend("Minimize"));
    document.getElementById("wc-max").addEventListener("click", () => backend("ToggleMaximize"));
    document.getElementById("wc-close").addEventListener("click", () => backend("Quit"));
}

// ─── 顶部下载栏 ──────────────────────────────────────────

const urlInput = document.getElementById("url-input");
const fieldError = document.getElementById("field-error");
const btnStart = document.getElementById("btn-start");

// 优先级已从 UI 移除，固定使用默认值 5。
const DEFAULT_PRIORITY = 5;

async function submitAdd() {
    const url = urlInput.value.trim().split("\n").map(s => s.trim()).filter(Boolean).join("\n");
    fieldError.classList.add("hidden");
    if (!url) {
        fieldError.textContent = "请输入下载链接";
        fieldError.classList.remove("hidden");
        return;
    }

    btnStart.disabled = true;
    try {
        for (const line of url.split("\n")) {
            await backend("AddDownload", line, DEFAULT_PRIORITY, 0);
        }
        urlInput.value = "";
        autoGrow();
        showToast("已添加下载任务");
    } catch (err) {
        fieldError.textContent = String(err).replace(/^error:?\s*/i, "");
        fieldError.classList.remove("hidden");
    } finally {
        btnStart.disabled = false;
    }
}

// textarea 高度自适应：粘贴多行内容时完整展开显示
function autoGrow() {
    urlInput.style.height = "auto";
    urlInput.style.height = `${urlInput.scrollHeight}px`;
}

btnStart.addEventListener("click", submitAdd);
// 多行输入框：Enter 换行，Ctrl+Enter 提交
urlInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        submitAdd();
    }
});
urlInput.addEventListener("input", autoGrow);

// ─── 侧边栏导航 ──────────────────────────────────────────

document.getElementById("side-nav").addEventListener("click", (e) => {
    // 保存目录设置项单独处理，不参与过滤器
    if (e.target.closest("#nav-savedir")) return;

    const item = e.target.closest(".nav-item[data-filter]");
    if (!item) return;
    document.querySelectorAll(".nav-item[data-filter]").forEach((t) => t.classList.remove("active"));
    item.classList.add("active");
    currentFilter = item.dataset.filter;
    render();
});

// ─── 保存目录设置 ────────────────────────────────────────

const savedirPath = document.getElementById("savedir-path");

async function refreshSaveDir() {
    try {
        const dir = await backend("GetSaveDir");
        if (dir) {
            savedirPath.textContent = dir;
            savedirPath.title = dir; // 悬停看完整路径
        }
    } catch (err) { /* ignore */ }
}

document.getElementById("nav-savedir").addEventListener("click", async () => {
    try {
        const dir = await backend("ChooseSaveDir");
        if (dir) {
            // 目录与当前一致时后端直接返回，不写配置
            if (dir === savedirPath.textContent) {
                showToast("保存目录未变化");
            } else {
                savedirPath.textContent = dir;
                savedirPath.title = dir;
                showToast("保存目录已更新");
            }
        }
    } catch (err) {
        showToast(String(err).replace(/^error:?\s*/i, ""), "error");
    }
});

refreshSaveDir();

// ─── 剪贴板自动识别 ──────────────────────────────────────

async function tryPasteClipboardLink() {
    try {
        const link = await backend("ReadClipboardLink");
        if (link && !urlInput.value.trim()) {
            urlInput.value = link;
            showToast("已从剪贴板识别到下载链接");
        }
    } catch (err) {
        // Clipboard unavailable or not a link; ignore silently.
    }
}

// ─── 数据轮询（500ms，与 TUI 保持一致） ──────────────────

// 由 downloads 数组本地聚合统计，省去一次跨进程绑定调用。
function computeStats(dl) {
    const stats = {
        total: dl.length, active: 0, queued: 0, paused: 0,
        completed: 0, failed: 0, cancelled: 0, totalBPS: 0,
    };
    for (const d of dl) {
        switch (d.status) {
            case "active": stats.active++; stats.totalBPS += d.speedBPS || 0; break;
            case "queued": case "pending": case "initializing": stats.queued++; break;
            case "paused": stats.paused++; break;
            case "completed": stats.completed++; break;
            case "failed": stats.failed++; break;
            case "cancelled": stats.cancelled++; break;
        }
    }
    return stats;
}

// 固定间隔轮询：不管上一次请求耗时多少，都按固定节拍刷新，
// 避免定时器漂移造成的渲染忽快忽慢。
async function poll() {
    try {
        const dl = (await backend("GetDownloads")) || [];
        downloads = dl;
        renderStats(computeStats(dl));
        render();
    } catch (err) {
        // Backend not ready yet; retry silently.
    }
    setTimeout(poll, 500);
}

initTheme();
initWindowControls();
poll();
tryPasteClipboardLink();
