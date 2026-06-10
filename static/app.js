const state = {
  categories: [],
  category: "default",
  note: "",
  dirty: false,
  expanded: {},
  selectedNotes: {},
  renderSeq: 0,
  autoSaveTimer: null,
  autoSaving: false,
  autoSaveAgain: false,
  lastSavedKey: "",
  lastSavedContent: "",
  fullscreen: false
};

const basePath = (window.APP_BASE_PATH || "").replace(/\/$/, "");

const el = {
  category: document.querySelector("#categorySelect"),
  move: document.querySelector("#moveSelect"),
  noteName: document.querySelector("#noteName"),
  editor: document.querySelector("#editor"),
  preview: document.querySelector("#preview"),
  tree: document.querySelector("#tree"),
  status: document.querySelector("#status"),
  importFile: document.querySelector("#importFile"),
  imageModal: document.querySelector("#imageModal"),
  imageGrid: document.querySelector("#imageGrid"),
  imageManagerModal: document.querySelector("#imageManagerModal"),
  imageManagerGrid: document.querySelector("#imageManagerGrid"),
  imSelectAll: document.querySelector("#imSelectAll"),
  imSelectedCount: document.querySelector("#imSelectedCount"),
  imDeleteSelected: document.querySelector("#imDeleteSelected"),
  contextMenu: document.querySelector("#contextMenu"),
  imageHoverPreview: document.querySelector("#imageHoverPreview"),
  viewToggle: document.querySelector(".view-toggle"),
  viewToggleButtons: document.querySelectorAll(".view-toggle__btn"),
  tocPanel: document.querySelector("#tocPanel"),
  tocList: document.querySelector("#tocList")
};

document.querySelector("#saveBtn").addEventListener("click", saveNote);
document.querySelector("#newNoteBtn").addEventListener("click", newNote);
document.querySelector("#refreshBtn").addEventListener("click", loadTree);
document.querySelector("#addCategoryBtn").addEventListener("click", addCategory);
document.querySelector("#moveBtn").addEventListener("click", moveNote);
document.querySelector("#importBtn").addEventListener("click", () => el.importFile.click());
document.querySelector("#exportBtn").addEventListener("click", exportCurrent);
document.querySelector("#backupBtn").addEventListener("click", backupAll);
document.querySelector("#insertImageBtn").addEventListener("click", openImagePicker);
document.querySelector("#imageManagerBtn").addEventListener("click", openImageManager);
document.querySelector("#closeImageManagerModal").addEventListener("click", closeImageManager);
document.querySelector("#deleteImageItem").addEventListener("click", deleteContextImage);
el.imSelectAll.addEventListener("change", toggleSelectAllImages);
el.imDeleteSelected.addEventListener("click", deleteSelectedImages);
document.querySelector("#embedHtmlBtn").addEventListener("click", embedHTML);
document.querySelector("#embedVideoBtn").addEventListener("click", embedVideo);
document.querySelector("#closeImageModal").addEventListener("click", closeImagePicker);
document.querySelector("#newFromFolderItem").addEventListener("click", newFromFolder);
document.querySelector("#restoreNoteItem").addEventListener("click", restoreContextNote);
document.querySelector("#renameNoteItem").addEventListener("click", renameContextNote);
document.querySelector("#deleteNoteItem").addEventListener("click", deleteContextNote);
document.querySelector("#renameCategoryItem").addEventListener("click", renameContextCategory);
document.querySelector("#moveCategoryUpItem").addEventListener("click", () => reorderContextCategory("up"));
document.querySelector("#moveCategoryDownItem").addEventListener("click", () => reorderContextCategory("down"));
document.querySelector("#deleteCategoryItem").addEventListener("click", deleteContextCategory);
el.viewToggleButtons.forEach(btn => {
  btn.addEventListener("click", () => {
    const mode = btn.dataset.viewMode || "split";
    setViewMode(mode);
  });
});
el.category.addEventListener("change", async () => {
  const previousCategory = state.category;
  if (state.dirty) {
    await saveNote({ category: previousCategory, silent: true, refresh: false });
    state.dirty = false;
  }
  state.category = el.category.value || "default";
  syncMoveOptions();
  renderTree();
});
el.editor.addEventListener("input", () => {
  state.dirty = true;
  renderPreview();
  scheduleAutoSave();
  // 实时同步滚动
  syncScrollOnEdit();
});
el.noteName.addEventListener("input", () => {
  state.note = normalizeMarkdownName(el.noteName.value);
  state.dirty = true;
  scheduleAutoSave();
});
el.importFile.addEventListener("change", importFile);
el.editor.addEventListener("paste", pasteImage);
document.addEventListener("click", () => {
  el.contextMenu.hidden = true;
});
document.addEventListener("keydown", event => {
  if (event.key === "Escape") {
    el.contextMenu.hidden = true;
    closeImagePicker();
    if (state.fullscreen) {
      // 在全屏模式下按 Esc 退出全屏
      state.fullscreen = false;
      document.body.classList.remove("fullscreen-mode");
      setViewMode("split");
    }
  }
});

window.addEventListener("beforeunload", event => {
  if (!state.dirty) return;
  event.preventDefault();
  event.returnValue = "";
});

loadTree().then(() => {
  renderPreview();
});

setViewMode("split");

async function loadTree() {
  const data = await api("/api/tree");
  state.categories = data;
  if (!data.some(c => c.name === state.category)) {
    state.category = data.some(c => c.name === "default") ? "default" : (data[0]?.name || "default");
  }
  for (const cat of state.categories) {
    if (state.expanded[cat.name] === undefined) state.expanded[cat.name] = true;
    if (state.expanded[`${cat.name}/notes`] === undefined) state.expanded[`${cat.name}/notes`] = true;
    if (state.expanded[`${cat.name}/img`] === undefined) state.expanded[`${cat.name}/img`] = false;
  }
  renderCategoryOptions();
  renderTree();
  setStatus("目录已刷新");
}

function renderCategoryOptions() {
  el.category.innerHTML = "";
  for (const cat of state.categories) {
    const opt = document.createElement("option");
    opt.value = cat.name;
    opt.textContent = cat.name;
    opt.selected = cat.name === state.category;
    el.category.appendChild(opt);
  }
  syncMoveOptions();
}

function syncMoveOptions() {
  el.move.innerHTML = "";
  for (const cat of state.categories) {
    const opt = document.createElement("option");
    opt.value = cat.name;
    opt.textContent = cat.name;
    opt.selected = cat.name !== state.category;
    el.move.appendChild(opt);
  }
}

function renderTree() {
  el.tree.innerHTML = "";
  for (const cat of state.categories) {
    const box = document.createElement("section");
    box.className = "cat";
    box.appendChild(folderRow({
      key: cat.name,
      label: cat.name,
      count: cat.notes.length,
      level: 0,
      dropCategory: cat.name,
      selected: cat.name === state.category,
      folderType: "category",
      draggable: cat.name !== "default" && cat.name !== "回收站"
    }));
    if (state.expanded[cat.name]) {
      box.appendChild(folderRow({
        key: `${cat.name}/notes`,
        label: "notes",
        count: cat.notes.length,
        level: 1,
        dropCategory: cat.name,
        folderType: "notes"
      }));
      if (state.expanded[`${cat.name}/notes`]) {
        const list = document.createElement("div");
        list.className = "folder-children";
        if (cat.notes.length === 0) {
          const empty = document.createElement("div");
          empty.className = "empty";
          empty.textContent = "暂无 Markdown";
          list.appendChild(empty);
        }
        for (const note of cat.notes) {
          const btn = document.createElement("button");
          btn.className = "note-item";
          const selected = isNoteSelected(note.category, note.name);
          if (note.category === state.category && note.name === state.note) btn.classList.add("active");
          if (selected) btn.classList.add("selected");
          btn.innerHTML = `
            <input type="checkbox" ${selected ? "checked" : ""} aria-label="选择 ${escapeAttr(note.name)}">
            <span>${escapeHTML(note.name)}</span>
          `;
          btn.draggable = true;
          btn.dataset.category = note.category;
          btn.dataset.name = note.name;
          const checkbox = btn.querySelector("input");
          checkbox.addEventListener("click", async event => {
            event.stopPropagation();
            await toggleNoteSelection(note.category, note.name, checkbox.checked);
            if (checkbox.checked) {
              await loadNoteIntoEditor(note.category, note.name);
            } else {
              renderTree();
            }
          });
          btn.addEventListener("dragstart", event => {
            event.dataTransfer.effectAllowed = "move";
            event.dataTransfer.setData("application/json", JSON.stringify({
              category: note.category,
              name: note.name
            }));
            event.dataTransfer.setData("text/plain", `${note.category}/${note.name}`);
            btn.classList.add("dragging");
          });
          btn.addEventListener("dragend", () => btn.classList.remove("dragging"));
          btn.addEventListener("contextmenu", event => {
            event.preventDefault();
            showContextMenu(event, note.category, note.name);
          });
          btn.addEventListener("click", () => openNote(note.category, note.name));
          list.appendChild(btn);
        }
        box.appendChild(list);
      }
      box.appendChild(folderRow({
        key: `${cat.name}/img`,
        label: "img",
        count: cat.images?.length || 0,
        level: 1,
        dropCategory: cat.name
      }));
      if (state.expanded[`${cat.name}/img`]) {
        const list = document.createElement("div");
        list.className = "folder-children image-tree";
        const images = cat.images || [];
        if (images.length === 0) {
          const empty = document.createElement("div");
          empty.className = "empty";
          empty.textContent = "暂无图片";
          list.appendChild(empty);
        }
        for (const image of images) {
          const btn = document.createElement("button");
          btn.type = "button";
          btn.className = "image-tree-item";
          btn.innerHTML = `
            <img src="${escapeAttr(image.url)}" alt="${escapeAttr(image.name)}">
            <span>${escapeHTML(image.name)}</span>
          `;
          attachImageHover(btn, image);
          btn.addEventListener("click", async event => {
            event.stopPropagation();
            await insertExistingImage(image);
          });
          btn.addEventListener("contextmenu", event => {
            event.preventDefault();
            showImageContextMenu(event, image.category, image.name);
          });
          list.appendChild(btn);
        }
        box.appendChild(list);
      }
    }
    el.tree.appendChild(box);
  }
}

function folderRow({ key, label, count, level, dropCategory, selected = false, folderType = "", draggable = false }) {
  const row = document.createElement("button");
  row.type = "button";
  row.className = `folder-row level-${level}`;
  if (selected) row.classList.add("selected-cat");
  row.dataset.category = dropCategory;
  row.dataset.folderType = folderType;
  row.draggable = draggable;
  row.innerHTML = `
      <span class="folder-left">
        <span class="twisty">${state.expanded[key] ? "▾" : "▸"}</span>
      <span class="folder-icon" aria-hidden="true"></span>
      <span>${escapeHTML(label)}</span>
    </span>
    <small>${count}</small>
  `;
  row.addEventListener("click", () => {
    state.category = dropCategory || state.category || "default";
    el.category.value = state.category;
    syncMoveOptions();
    state.expanded[key] = !state.expanded[key];
    renderTree();
  });
  row.addEventListener("dragover", event => {
    event.preventDefault();
    row.classList.add("drop-target");
    event.dataTransfer.dropEffect = "move";
  });
  row.addEventListener("dragleave", () => row.classList.remove("drop-target"));
  row.addEventListener("drop", async event => {
    event.preventDefault();
    row.classList.remove("drop-target");
    const categoryRaw = event.dataTransfer.getData("application/x-category");
    if (categoryRaw) {
      await moveDraggedCategory(categoryRaw, dropCategory);
      return;
    }
    await moveDraggedNote(event, dropCategory);
  });
  row.addEventListener("dragstart", event => {
    if (!draggable) return;
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData("application/x-category", dropCategory);
    event.dataTransfer.setData("text/plain", dropCategory);
    row.classList.add("dragging");
  });
  row.addEventListener("dragend", () => row.classList.remove("dragging"));
  row.addEventListener("contextmenu", event => {
    event.preventDefault();
    if (folderType === "notes" && dropCategory !== "回收站") {
      showFolderContextMenu(event, dropCategory);
      return;
    }
    if (folderType === "category") {
      showCategoryContextMenu(event, dropCategory);
    }
  });
  return row;
}

async function moveDraggedNote(event, toCategory) {
  const raw = event.dataTransfer.getData("application/json");
  if (!raw) return;
  const dragged = JSON.parse(raw);
  if (!dragged.name || dragged.category === toCategory) {
    setStatus("已在当前分类，无需移动");
    return;
  }
  if (state.dirty && dragged.category === state.category && dragged.name === state.note) {
    await saveNote({ silent: true, refresh: false });
  }
  const data = await api("/api/move", {
    method: "POST",
    body: JSON.stringify({ fromCategory: dragged.category, toCategory, name: dragged.name })
  });
  state.expanded[toCategory] = true;
  state.expanded[`${toCategory}/notes`] = true;
  await loadTree();
  if (dragged.category === state.category && dragged.name === state.note) {
    await openNote(data.category, data.name);
  }
  setStatus(`已拖拽移动到 ${data.category}/notes/${data.name}`);
}

async function moveDraggedCategory(fromCategory, toCategory) {
  if (!fromCategory || !toCategory || fromCategory === toCategory) {
    setStatus("已在当前分类，无需移动");
    return;
  }
  if (fromCategory === "default" || fromCategory === "回收站" || toCategory === "回收站") {
    alert("default 和回收站分类不支持这样拖动");
    return;
  }
  if (!confirm(`把分类「${fromCategory}」下的所有 Markdown 和相关图片移动合并到「${toCategory}」吗？`)) return;
  await api("/api/category-move", {
    method: "POST",
    body: JSON.stringify({ from: fromCategory, to: toCategory })
  });
  if (state.category === fromCategory) {
    state.category = toCategory;
    el.category.value = toCategory;
  }
  state.expanded[toCategory] = true;
  state.expanded[`${toCategory}/notes`] = true;
  await loadTree();
  setStatus(`已把分类 ${fromCategory} 合并到 ${toCategory}`);
}

async function openNote(category, name) {
  if (state.dirty) await saveNote({ silent: true, refresh: false });
  await loadNoteIntoEditor(category, name);
}

async function loadNoteIntoEditor(category, name) {
  const data = await api(`/api/note?category=${encodeURIComponent(category)}&name=${encodeURIComponent(name)}`);
  state.category = data.category;
  state.note = data.name;
  selectOnlyNote(data.category, data.name);
  el.category.value = state.category;
  el.noteName.value = state.note;
  el.editor.value = data.content;
  state.dirty = false;
  state.lastSavedKey = noteKey(state.category, state.note);
  state.lastSavedContent = data.content;
  syncMoveOptions();
  renderPreview();
  renderTree();
  setStatus(`已打开 ${state.category}/${state.note}`);
}

async function reloadCurrentNote() {
  if (!state.category || !state.note) return false;
  try {
    await loadNoteIntoEditor(state.category, state.note);
    return true;
  } catch (err) {
    return false;
  }
}

async function newNote() {
  if (state.dirty) {
    await saveNote({ silent: true, refresh: false });
  }
  const name = prompt("新笔记文件名", "新笔记.md");
  if (!name) return;
  state.category = el.category.value || state.category || "default";
  state.note = normalizeMarkdownName(name);
  selectOnlyNote(state.category, state.note);
  el.noteName.value = state.note;
  el.editor.value = `# ${state.note.replace(/\.md$/i, "")}\n\n`;
  state.dirty = true;
  renderPreview();
  setStatus("新笔记待保存");
  scheduleAutoSave(0);
}

async function saveNote(options = {}) {
  const { category: forcedCategory = "", silent = false, refresh = true } = options;
  clearAutoSaveTimer();
  const name = normalizeMarkdownName(el.noteName.value);
  if (!name) {
    if (!silent) alert("请填写文件名");
    return;
  }
  const category = forcedCategory || el.category.value || state.category;
  const content = el.editor.value;
  const previousKey = state.lastSavedKey;
  const nextKey = noteKey(category, name);
  await api("/api/note", {
    method: "POST",
    body: JSON.stringify({ category, name, content })
  });
  state.category = category;
  state.note = name;
  el.noteName.value = name;
  state.lastSavedKey = nextKey;
  state.lastSavedContent = content;
  state.dirty = state.lastSavedContent !== el.editor.value || state.lastSavedKey !== noteKey(el.category.value || state.category, normalizeMarkdownName(el.noteName.value));
  if (refresh || previousKey !== nextKey) await loadTree();
  setStatus(silent ? `已自动保存 ${category}/notes/${name}` : `已保存 ${category}/notes/${name}`);
}

function clearAutoSaveTimer() {
  if (!state.autoSaveTimer) return;
  clearTimeout(state.autoSaveTimer);
  state.autoSaveTimer = null;
}

function scheduleAutoSave(delay = 1000) {
  clearAutoSaveTimer();
  state.autoSaveTimer = setTimeout(autoSaveNote, delay);
}

async function autoSaveNote() {
  if (!state.dirty) return;
  if (state.autoSaving) {
    state.autoSaveAgain = true;
    return;
  }
  const name = normalizeMarkdownName(el.noteName.value);
  if (!name) return;
  state.autoSaving = true;
  setStatus("正在自动保存...");
  try {
    await saveNote({ silent: true, refresh: false });
  } catch (err) {
    state.dirty = true;
    setStatus(`自动保存失败：${err.message}`);
  } finally {
    state.autoSaving = false;
    if (state.autoSaveAgain) {
      state.autoSaveAgain = false;
      scheduleAutoSave();
    }
  }
}

async function addCategory() {
  const name = prompt("分类名称");
  if (!name) return;
  const existing = state.categories.find(cat => cat.name === name.trim());
  if (existing) {
    if (!confirm(`分类「${existing.name}」已经存在，是否切换到该分类？`)) return;
    state.category = existing.name;
    el.category.value = existing.name;
    renderTree();
    setStatus(`已切换到分类 ${existing.name}`);
    return;
  }
  const data = await api("/api/category", {
    method: "POST",
    body: JSON.stringify({ name })
  });
  state.category = data.name;
  await loadTree();
  setStatus(`已创建分类 ${data.name}`);
}

async function moveNote() {
  if (!state.note && el.noteName.value) state.note = normalizeMarkdownName(el.noteName.value);
  if (!state.note) {
    alert("请先打开或保存一篇 Markdown");
    return;
  }
  const to = el.move.value;
  if (!to || to === state.category) {
    alert("请选择不同的目标分类");
    return;
  }
  if (state.dirty) await saveNote({ silent: true, refresh: false });
  const data = await api("/api/move", {
    method: "POST",
    body: JSON.stringify({ fromCategory: state.category, toCategory: to, name: state.note })
  });
  await loadTree();
  await openNote(data.category, data.name);
  setStatus(`已移动到 ${data.category}`);
}

async function importFile() {
  const file = el.importFile.files[0];
  if (!file) return;
  const category = el.category.value || state.category;
  const overwrite = confirm("导入时如果遇到同名分类、Markdown 或图片，是否覆盖？\n\n确定：覆盖同名内容。\n取消：自动改名，保留原内容。");
  const form = new FormData();
  form.append("category", category);
  form.append("overwrite", overwrite ? "true" : "false");
  form.append("file", file);
  const result = await api("/api/import", { method: "POST", body: form });
  el.importFile.value = "";
  await loadTree();
  if (isMarkdownFile(file.name)) {
    await loadNoteIntoEditor(category, normalizeMarkdownName(file.name));
  } else {
    await reloadCurrentNote();
  }
  const first = result.written?.find(item => item.kind === "notes") || result.written?.[0];
  const detail = first ? `，写入：${first.absPath}，${first.size} 字节，覆盖=${first.overwritten ? "是" : "否"}` : "";
  setStatus(`已导入 ${file.name}${detail}`);
}

function exportCurrent() {
  const category = el.category.value || state.category || "default";
  const selected = selectedNotesFor(category);
  if (selected.length === 0) {
    alert("请先在右侧目录树勾选要导出的 Markdown");
    return;
  }
  const params = new URLSearchParams();
  params.set("category", category);
  for (const name of selected) params.append("name", name);
  const url = appURL(`/export?${params.toString()}`);
  window.location.href = url;
}

function backupAll() {
  window.location.href = appURL("/backup");
}

function noteKey(category, name) {
  return `${category}\u0000${name}`;
}

function isNoteSelected(category, name) {
  return Boolean(state.selectedNotes[noteKey(category, name)]);
}

async function toggleNoteSelection(category, name, selected) {
  const key = noteKey(category, name);
  if (selected) {
    if (state.dirty) {
      await saveNote({ silent: true, refresh: false });
      state.dirty = false;
    }
    state.selectedNotes = {};
    state.selectedNotes[key] = { category, name };
    return;
  }
  delete state.selectedNotes[key];
}

function selectOnlyNote(category, name) {
  state.selectedNotes = {};
  toggleNoteSelection(category, name, true);
}

function selectedNotesFor(category) {
  return Object.values(state.selectedNotes)
    .filter(item => item.category === category)
    .map(item => item.name)
    .sort((a, b) => a.localeCompare(b));
}

async function openImagePicker() {
  const images = (await api("/api/images")) || [];
  el.imageGrid.innerHTML = "";
  if (images.length === 0) {
    const empty = document.createElement("div");
    empty.className = "empty";
    empty.textContent = "所有分类的 img 文件夹中暂无图片";
    el.imageGrid.appendChild(empty);
  }
  for (const image of images) {
    const btn = document.createElement("button");
    btn.className = "image-card";
    btn.innerHTML = `
      <img src="${escapeAttr(image.url)}" alt="${escapeAttr(image.name)}">
      <span>${escapeHTML(image.category)}</span>
      <strong>${escapeHTML(image.name)}</strong>
    `;
    attachImageHover(btn, image);
    btn.addEventListener("click", () => insertExistingImage(image));
    el.imageGrid.appendChild(btn);
  }
  el.imageModal.hidden = false;
}

function closeImagePicker() {
  el.imageModal.hidden = true;
  hideImageHover();
}

let imageManagerImages = [];
const imageSelection = new Set();

function imageKey(image) {
  return `${image.category}/${image.name}`;
}

function splitImageKey(key) {
  const idx = key.indexOf("/");
  return { category: key.slice(0, idx), name: key.slice(idx + 1) };
}

// 删除图片后，把当前打开文档正文里引用这张图的 ![]() 和 <img> 一并移除。
// 引用是相对路径（../img/名字），只对同分类的当前文档生效。返回是否有改动。
function removeImageRefsFromEditor(category, name) {
  const noteCategory = el.category.value || state.category;
  if (!state.note || category !== noteCategory) return false;
  const esc = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const original = el.editor.value;
  let next = original
    .replace(new RegExp(`<img\\b[^>]*?src\\s*=\\s*["'][^"']*?/${esc}(?![\\w.-])[^"']*?["'][^>]*?>\\n?`, "gi"), "")
    .replace(new RegExp(`!\\[[^\\]]*\\]\\([^)]*?/${esc}(?![\\w.-])[^)]*?\\)\\n?`, "gi"), "");
  if (next === original) return false;
  el.editor.value = next;
  state.dirty = true;
  return true;
}

async function openImageManager() {
  el.imageManagerModal.hidden = false;
  imageSelection.clear();
  await renderImageManager();
}

function closeImageManager() {
  el.imageManagerModal.hidden = true;
  hideImageHover();
}

async function renderImageManager() {
  imageManagerImages = (await api("/api/images")) || [];
  // 清理选中集合中已不存在的图片
  const validKeys = new Set(imageManagerImages.map(imageKey));
  for (const key of [...imageSelection]) {
    if (!validKeys.has(key)) imageSelection.delete(key);
  }
  el.imageManagerGrid.innerHTML = "";
  if (!imageManagerImages.length) {
    const empty = document.createElement("div");
    empty.className = "empty";
    empty.textContent = "所有分类的 img 文件夹中暂无图片";
    el.imageManagerGrid.appendChild(empty);
    updateImageManagerToolbar();
    return;
  }
  for (const image of imageManagerImages) {
    const key = imageKey(image);
    const card = document.createElement("div");
    card.className = "image-card image-manage-card";
    if (imageSelection.has(key)) card.classList.add("is-selected");
    card.innerHTML = `
      <input type="checkbox" class="im-card-check" ${imageSelection.has(key) ? "checked" : ""} aria-label="选择 ${escapeAttr(image.name)}">
      <img src="${escapeAttr(image.url)}" alt="${escapeAttr(image.name)}">
      <span>${escapeHTML(image.category)}</span>
      <strong title="${escapeAttr(image.name)}">${escapeHTML(image.name)}</strong>
      <small>${formatBytes(image.size)}</small>
      <button type="button" class="image-delete-btn">删除</button>
    `;
    attachImageHover(card.querySelector("img"), image);
    const check = card.querySelector(".im-card-check");
    check.addEventListener("click", event => event.stopPropagation());
    check.addEventListener("change", () => {
      if (check.checked) imageSelection.add(key);
      else imageSelection.delete(key);
      card.classList.toggle("is-selected", check.checked);
      updateImageManagerToolbar();
    });
    card.querySelector(".image-delete-btn").addEventListener("click", async () => {
      if (!confirm(`确定删除图片「${image.category}/${image.name}」吗？\n此操作不可恢复，引用它的文档会显示坏图。`)) return;
      try {
        await api("/api/image-delete", {
          method: "POST",
          body: JSON.stringify({ category: image.category, name: image.name })
        });
      } catch (err) {
        alert("删除失败：" + (err && err.message ? err.message : err));
        return;
      }
      imageSelection.delete(key);
      const edited = removeImageRefsFromEditor(image.category, image.name);
      setStatus(`已删除图片：${image.category}/${image.name}`);
      await renderImageManager();
      try { await loadTree(); } catch (e) {}
      renderPreview();
      if (edited) scheduleAutoSave();
    });
    el.imageManagerGrid.appendChild(card);
  }
  updateImageManagerToolbar();
}

function updateImageManagerToolbar() {
  const total = imageManagerImages.length;
  const selected = imageSelection.size;
  el.imSelectedCount.textContent = `已选 ${selected} / ${total} 张`;
  el.imDeleteSelected.disabled = selected === 0;
  el.imSelectAll.checked = total > 0 && selected === total;
  el.imSelectAll.indeterminate = selected > 0 && selected < total;
}

function toggleSelectAllImages() {
  if (el.imSelectAll.checked) {
    for (const image of imageManagerImages) imageSelection.add(imageKey(image));
  } else {
    imageSelection.clear();
  }
  for (const card of el.imageManagerGrid.querySelectorAll(".image-manage-card")) {
    const check = card.querySelector(".im-card-check");
    if (check) {
      check.checked = el.imSelectAll.checked;
      card.classList.toggle("is-selected", el.imSelectAll.checked);
    }
  }
  updateImageManagerToolbar();
}

async function deleteSelectedImages() {
  const keys = [...imageSelection];
  if (!keys.length) return;
  if (!confirm(`确定删除选中的 ${keys.length} 张图片吗？\n此操作不可恢复，引用它们的文档会显示坏图。`)) return;
  let failed = 0;
  let edited = false;
  for (const key of keys) {
    const { category, name } = splitImageKey(key);
    try {
      await api("/api/image-delete", {
        method: "POST",
        body: JSON.stringify({ category, name })
      });
      imageSelection.delete(key);
      if (removeImageRefsFromEditor(category, name)) edited = true;
    } catch (err) {
      failed++;
    }
  }
  setStatus(failed ? `已删除 ${keys.length - failed} 张，${failed} 张失败` : `已删除 ${keys.length} 张图片`);
  await renderImageManager();
  try { await loadTree(); } catch (e) {}
  renderPreview();
  if (edited) scheduleAutoSave();
}

function formatBytes(size) {
  const n = Number(size) || 0;
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

function attachImageHover(node, image) {
  node.addEventListener("mouseenter", event => showImageHover(event, image));
  node.addEventListener("mousemove", moveImageHover);
  node.addEventListener("mouseleave", hideImageHover);
}

function showImageHover(event, image) {
  if (!el.imageHoverPreview) return;
  el.imageHoverPreview.innerHTML = `
    <img src="${escapeAttr(image.url)}" alt="${escapeAttr(image.name)}">
    <span>${escapeHTML(image.category || state.category)} / ${escapeHTML(image.name)}</span>
  `;
  el.imageHoverPreview.hidden = false;
  moveImageHover(event);
}

function moveImageHover(event) {
  if (!el.imageHoverPreview || el.imageHoverPreview.hidden) return;
  const margin = 18;
  const rect = el.imageHoverPreview.getBoundingClientRect();
  let left = event.clientX + margin;
  let top = event.clientY + margin;
  if (left + rect.width > window.innerWidth - margin) {
    left = event.clientX - rect.width - margin;
  }
  if (top + rect.height > window.innerHeight - margin) {
    top = event.clientY - rect.height - margin;
  }
  el.imageHoverPreview.style.left = `${Math.max(margin, left)}px`;
  el.imageHoverPreview.style.top = `${Math.max(margin, top)}px`;
}

function hideImageHover() {
  if (!el.imageHoverPreview) return;
  el.imageHoverPreview.hidden = true;
}

async function insertExistingImage(image) {
  const category = el.category.value || state.category;
  const data = await api("/api/image-ref", {
    method: "POST",
    body: JSON.stringify({
      currentCategory: category,
      sourceCategory: image.category,
      name: image.name
    })
  });
  insertAtCursor(imageHTML(data.path, data.name, 480));
  state.dirty = true;
  renderPreview();
  scheduleAutoSave();
  closeImagePicker();
  state.expanded[category] = true;
  state.expanded[`${category}/img`] = true;
  await loadTree();
  setStatus(`已插入图片 ${data.name}`);
}

function embedHTML() {
  const html = prompt("输入要嵌入的 HTML 代码");
  if (!html) return;
  insertAtCursor(`<!-- html:start -->\n${html}\n<!-- html:end -->`);
  state.dirty = true;
  renderPreview();
  scheduleAutoSave();
  setStatus("已嵌入 HTML");
}

function embedVideo() {
  const rawURL = prompt("输入网络视频地址或 iframe src");
  if (!rawURL) return;
  const src = normalizeVideoURL(rawURL.trim());
  const width = parseInt(prompt("视频宽度", "640") || "640", 10);
  const height = parseInt(prompt("视频高度", "360") || "360", 10);
  insertAtCursor(`<iframe src="${escapeAttr(src)}" width="${safeSize(width, 640)}" height="${safeSize(height, 360)}" allowfullscreen loading="lazy"></iframe>`);
  state.dirty = true;
  renderPreview();
  scheduleAutoSave();
  setStatus("已嵌入网络视频");
}

function showImageContextMenu(event, category, name) {
  el.contextMenu.dataset.type = "image";
  el.contextMenu.dataset.category = category;
  el.contextMenu.dataset.name = name;
  document.querySelector("#newFromFolderItem").hidden = true;
  document.querySelector("#restoreNoteItem").hidden = true;
  document.querySelector("#renameNoteItem").hidden = true;
  document.querySelector("#deleteNoteItem").hidden = true;
  document.querySelector("#deleteImageItem").hidden = false;
  document.querySelector("#renameCategoryItem").hidden = true;
  document.querySelector("#moveCategoryUpItem").hidden = true;
  document.querySelector("#moveCategoryDownItem").hidden = true;
  document.querySelector("#deleteCategoryItem").hidden = true;
  el.contextMenu.style.left = `${event.clientX}px`;
  el.contextMenu.style.top = `${event.clientY}px`;
  el.contextMenu.hidden = false;
}

function showContextMenu(event, category, name) {
  el.contextMenu.dataset.type = "note";
  el.contextMenu.dataset.category = category;
  el.contextMenu.dataset.name = name;
  const isTrash = category === "回收站";
  document.querySelector("#newFromFolderItem").hidden = true;
  document.querySelector("#restoreNoteItem").hidden = !isTrash;
  document.querySelector("#renameNoteItem").hidden = isTrash;
  document.querySelector("#deleteNoteItem").hidden = isTrash;
  document.querySelector("#deleteImageItem").hidden = true;
  document.querySelector("#renameCategoryItem").hidden = true;
  document.querySelector("#moveCategoryUpItem").hidden = true;
  document.querySelector("#moveCategoryDownItem").hidden = true;
  document.querySelector("#deleteCategoryItem").hidden = true;
  el.contextMenu.style.left = `${event.clientX}px`;
  el.contextMenu.style.top = `${event.clientY}px`;
  el.contextMenu.hidden = false;
}

function showFolderContextMenu(event, category) {
  el.contextMenu.dataset.type = "folder";
  el.contextMenu.dataset.category = category;
  el.contextMenu.dataset.name = "";
  document.querySelector("#newFromFolderItem").hidden = false;
  document.querySelector("#restoreNoteItem").hidden = true;
  document.querySelector("#renameNoteItem").hidden = true;
  document.querySelector("#deleteNoteItem").hidden = true;
  document.querySelector("#deleteImageItem").hidden = true;
  document.querySelector("#renameCategoryItem").hidden = true;
  document.querySelector("#moveCategoryUpItem").hidden = true;
  document.querySelector("#moveCategoryDownItem").hidden = true;
  document.querySelector("#deleteCategoryItem").hidden = true;
  el.contextMenu.style.left = `${event.clientX}px`;
  el.contextMenu.style.top = `${event.clientY}px`;
  el.contextMenu.hidden = false;
}

function showCategoryContextMenu(event, category) {
  el.contextMenu.dataset.type = "category";
  el.contextMenu.dataset.category = category;
  el.contextMenu.dataset.name = "";
  document.querySelector("#newFromFolderItem").hidden = true;
  document.querySelector("#restoreNoteItem").hidden = true;
  document.querySelector("#renameNoteItem").hidden = true;
  document.querySelector("#deleteNoteItem").hidden = true;
  document.querySelector("#deleteImageItem").hidden = true;
  document.querySelector("#renameCategoryItem").hidden = category === "default" || category === "回收站";
  document.querySelector("#moveCategoryUpItem").hidden = false;
  document.querySelector("#moveCategoryDownItem").hidden = false;
  document.querySelector("#deleteCategoryItem").hidden = category === "default" || category === "回收站";
  el.contextMenu.style.left = `${event.clientX}px`;
  el.contextMenu.style.top = `${event.clientY}px`;
  el.contextMenu.hidden = false;
}

async function newFromFolder() {
  const category = el.contextMenu.dataset.category || "default";
  el.contextMenu.hidden = true;
  const name = normalizeMarkdownName(prompt("保存为 Markdown 文件名", el.noteName.value || "新笔记.md") || "");
  if (!name) return;
  await api("/api/note", {
    method: "POST",
    body: JSON.stringify({ category, name, content: el.editor.value })
  });
  state.category = category;
  state.note = "";
  el.category.value = category;
  el.noteName.value = "";
  el.editor.value = "";
  state.dirty = false;
  state.selectedNotes = {};
  state.expanded[category] = true;
  state.expanded[`${category}/notes`] = true;
  renderPreview();
  await loadTree();
  setStatus(`已保存 ${category}/notes/${name}，编辑器已清空`);
}

async function restoreContextNote() {
  const name = el.contextMenu.dataset.name;
  el.contextMenu.hidden = true;
  if (!name) return;
  const data = await api("/api/restore", {
    method: "POST",
    body: JSON.stringify({ name })
  });
  state.category = data.category;
  state.note = data.name;
  selectOnlyNote(data.category, data.name);
  await loadTree();
  await openNote(data.category, data.name);
  setStatus(`已恢复到 ${data.category}/notes/${data.name}`);
}

async function deleteContextNote() {
  const category = el.contextMenu.dataset.category;
  const name = el.contextMenu.dataset.name;
  el.contextMenu.hidden = true;
  if (!category || !name) return;
  if (!confirm(`确定把 ${category}/notes/${name} 移动到回收站吗？回收站默认保留 7 天。`)) return;
  await api(`/api/note?category=${encodeURIComponent(category)}&name=${encodeURIComponent(name)}`, {
    method: "DELETE"
  });
  if (state.category === category && state.note === name) {
    state.note = "";
    el.noteName.value = "";
    el.editor.value = "";
    state.dirty = false;
    renderPreview();
  }
  toggleNoteSelection(category, name, false);
  await loadTree();
  state.expanded["回收站"] = true;
  state.expanded["回收站/notes"] = true;
  setStatus(`已移动到回收站：${name}`);
}

async function deleteContextImage() {
  const category = el.contextMenu.dataset.category;
  const name = el.contextMenu.dataset.name;
  el.contextMenu.hidden = true;
  if (!category || !name) return;
  if (!confirm(`确定删除图片「${category}/${name}」吗？\n此操作不可恢复，引用它的文档会显示坏图。`)) return;
  try {
    await api("/api/image-delete", {
      method: "POST",
      body: JSON.stringify({ category, name })
    });
  } catch (err) {
    alert("删除失败：" + (err && err.message ? err.message : err));
    return;
  }
  const edited = removeImageRefsFromEditor(category, name);
  await loadTree();
  renderPreview();
  if (edited) scheduleAutoSave();
  if (!el.imageManagerModal.hidden) await renderImageManager();
  setStatus(`已删除图片：${category}/${name}`);
}

async function deleteContextCategory() {
  const category = el.contextMenu.dataset.category;
  el.contextMenu.hidden = true;
  if (!category || category === "default" || category === "回收站") return;
  if (!confirm(`确定删除分类「${category}」吗？其中 Markdown 会进入回收站并保留 7 天。`)) return;
  await api("/api/category-delete", {
    method: "POST",
    body: JSON.stringify({ name: category })
  });
  if (state.category === category) {
    state.category = "default";
    state.note = "";
    el.category.value = "default";
    el.noteName.value = "";
    el.editor.value = "";
    state.selectedNotes = {};
    renderPreview();
  }
  await loadTree();
  setStatus(`已删除分类 ${category}`);
}

async function renameContextCategory() {
  const oldName = el.contextMenu.dataset.category;
  el.contextMenu.hidden = true;
  if (!oldName || oldName === "default" || oldName === "回收站") return;
  const newName = (prompt("新的分类名称", oldName) || "").trim();
  if (!newName || newName === oldName) return;
  const data = await api("/api/category-rename", {
    method: "POST",
    body: JSON.stringify({ oldName, newName })
  });
  if (state.category === oldName) {
    state.category = data.name;
    el.category.value = data.name;
  }
  if (state.expanded[oldName] !== undefined) {
    state.expanded[data.name] = state.expanded[oldName];
    delete state.expanded[oldName];
  }
  if (state.expanded[`${oldName}/notes`] !== undefined) {
    state.expanded[`${data.name}/notes`] = state.expanded[`${oldName}/notes`];
    delete state.expanded[`${oldName}/notes`];
  }
  if (state.expanded[`${oldName}/img`] !== undefined) {
    state.expanded[`${data.name}/img`] = state.expanded[`${oldName}/img`];
    delete state.expanded[`${oldName}/img`];
  }
  const nextSelected = {};
  for (const item of Object.values(state.selectedNotes)) {
    if (item.category === oldName) item.category = data.name;
    nextSelected[noteKey(item.category, item.name)] = item;
  }
  state.selectedNotes = nextSelected;
  await loadTree();
  setStatus(`已重命名分类为 ${data.name}`);
}

async function reorderContextCategory(direction) {
  const name = el.contextMenu.dataset.category;
  el.contextMenu.hidden = true;
  if (!name) return;
  await api("/api/category-reorder", {
    method: "POST",
    body: JSON.stringify({ name, direction })
  });
  await loadTree();
  setStatus(direction === "up" ? `已上移分类 ${name}` : `已下移分类 ${name}`);
}

async function renameContextNote() {
  const category = el.contextMenu.dataset.category;
  const oldName = el.contextMenu.dataset.name;
  el.contextMenu.hidden = true;
  if (!category || !oldName) return;
  const newName = normalizeMarkdownName(prompt("新的 Markdown 文件名", oldName) || "");
  if (!newName || newName === oldName) return;
  const data = await api("/api/rename", {
    method: "POST",
    body: JSON.stringify({ category, oldName, newName })
  });
  if (state.category === category && state.note === oldName) {
    state.note = data.name;
    el.noteName.value = data.name;
  }
  if (isNoteSelected(category, oldName)) {
    toggleNoteSelection(category, oldName, false);
    toggleNoteSelection(category, data.name, true);
  }
  await loadTree();
  setStatus(`已重命名为 ${data.name}`);
}

async function pasteImage(event) {
  const items = event.clipboardData?.items || [];
  for (const item of items) {
    if (!item.type.startsWith("image/")) continue;
    event.preventDefault();
    const file = item.getAsFile();
    const category = el.category.value || state.category;
    const form = new FormData();
    form.append("category", category);
    form.append("image", file, `paste.${item.type.split("/")[1] || "png"}`);
    setStatus("正在保存粘贴图片...");
    const data = await api("/api/upload-image", { method: "POST", body: form, json: false });
    insertAtCursor(imageHTML(data.path, data.name, 480));
    state.dirty = true;
    renderPreview();
    scheduleAutoSave();
    state.expanded[category] = true;
    state.expanded[`${category}/img`] = true;
    await loadTree();
    setStatus(`图片已保存到 ${category}/img/${data.name}`);
    return;
  }
}

function insertAtCursor(text) {
  const start = el.editor.selectionStart;
  const end = el.editor.selectionEnd;
  const before = el.editor.value.slice(0, start);
  const after = el.editor.value.slice(end);
  const prefix = before.endsWith("\n") || before.length === 0 ? "" : "\n";
  const suffix = after.startsWith("\n") ? "" : "\n";
  el.editor.value = before + prefix + text + suffix + after;
  const pos = start + prefix.length + text.length + suffix.length;
  el.editor.setSelectionRange(pos, pos);
  el.editor.focus();
}

async function renderPreview() {
  const seq = ++state.renderSeq;
  const markdown = el.editor.value || "";
  try {
    const data = await api("/api/render", {
      method: "POST",
      body: JSON.stringify({ markdown })
    });
    if (seq !== state.renderSeq) return;
    el.preview.innerHTML = addLineNumbersToHTML(data.html, markdown);
    if (window.MathJax && window.MathJax.typesetPromise) {
      window.MathJax.typesetPromise([el.preview]);
    }
  } catch (err) {
    if (seq !== state.renderSeq) return;
    el.preview.innerHTML = markdownToHTML(markdown, true);
    if (window.MathJax && window.MathJax.typesetPromise) {
      window.MathJax.typesetPromise([el.preview]);
    }
  }
  for (const img of el.preview.querySelectorAll("img")) {
    const src = img.getAttribute("src") || "";
    if (src.startsWith("../img/")) {
      img.dataset.noteSrc = src;
      img.src = appURL(`/files/${encodeURIComponent(el.category.value || state.category)}/img/${encodeURIComponent(src.split("/").pop())}`);
    }
  }
  enhanceResizableMedia();
  setupSyncScroll();
  buildTOC();
}

// ── 全屏目录（TOC） ──────────────────────────────────────────────
function buildTOC() {
  if (!el.tocList) return;
  const headings = el.preview.querySelectorAll("h1, h2, h3, h4, h5, h6");
  el.tocList.innerHTML = "";
  if (!headings.length) {
    el.tocList.innerHTML = '<li class="toc-panel__empty">当前文档没有标题</li>';
    updateTocVisibility();
    return;
  }
  headings.forEach((h, i) => {
    if (!h.id) h.id = `toc-h-${i}`;
    const level = h.tagName.substring(1);
    const li = document.createElement("li");
    const a = document.createElement("a");
    a.textContent = (h.textContent || "").trim() || `标题 ${i + 1}`;
    a.title = a.textContent;
    a.dataset.level = level;
    a.dataset.target = h.id;
    a.addEventListener("click", (ev) => {
      ev.preventDefault();
      const target = el.preview.querySelector(`#${CSS.escape(h.id)}`);
      if (!target) return;
      const offset = target.getBoundingClientRect().top - el.preview.getBoundingClientRect().top;
      el.preview.scrollTop += offset - 8;
    });
    li.appendChild(a);
    el.tocList.appendChild(li);
  });
  updateTocVisibility();
}

function updateTocVisibility() {
  if (!el.tocPanel) return;
  // 预览可见且文档有标题时，在编辑区右侧显示目录（全屏和普通模式均显示）
  const hasHeadings = !!(el.tocList && el.tocList.querySelector("a"));
  const show = !el.preview.hidden && hasHeadings;
  el.tocPanel.hidden = !show;
  document.body.classList.toggle("toc-visible", show);
}

function addLineNumbersToHTML(html, markdown) {
  if (!html || !markdown) return html;
  
  try {
    // 1. 解析 markdown 源码，识别每个块级元素在源码中的起始行号（0-based）
    const lines = markdown.split(/\r?\n/);
    const blockStarts = []; // 每个渲染块对应的 markdown 起始行号
    let inCodeBlock = false;
    let inHTMLBlock = false;
    let inList = false;       // 处于列表块中（松散列表跨单个空行仍算同一块）
    let pendingBlock = false; // 段落/引用等待结束
    let consecutiveEmptyCount = 0;

    for (let i = 0; i < lines.length; i++) {
      const raw = lines[i];
      const trimmed = raw.trim();

      // 代码块：整体算一个块
      if (trimmed.startsWith('```')) {
        if (!inCodeBlock) {
          inCodeBlock = true;
          blockStarts.push(i);
          pendingBlock = false;
          inList = false;
          consecutiveEmptyCount = 0;
        } else {
          inCodeBlock = false;
        }
        continue;
      }
      if (inCodeBlock) continue;

      // HTML 嵌入块
      if (trimmed === '<!-- html:start -->') {
        inHTMLBlock = true;
        blockStarts.push(i);
        pendingBlock = false;
        inList = false;
        consecutiveEmptyCount = 0;
        continue;
      }
      if (trimmed === '<!-- html:end -->') {
        inHTMLBlock = false;
        continue;
      }
      if (inHTMLBlock) continue;

      // 空行：结束段落；单个空行不结束列表（松散列表），连续空行才结束
      if (trimmed === '') {
        consecutiveEmptyCount++;
        pendingBlock = false;
        if (consecutiveEmptyCount >= 2) inList = false;
        continue;
      }

      // Setext 标题下划线：紧跟段落（pendingBlock）的纯 = 或 - 行，归入上一块，不新建块
      if (pendingBlock && !inList && /^(=+|-+)\s*$/.test(trimmed)) {
        pendingBlock = false;
        consecutiveEmptyCount = 0;
        continue;
      }

      // 列表项：整个列表（含松散列表）算一个块
      if (/^\s*[-*+]\s+/.test(trimmed) || /^\s*\d+\.\s+/.test(trimmed)) {
        if (!inList) {
          blockStarts.push(i);
          inList = true;
        }
        pendingBlock = false;
        consecutiveEmptyCount = 0;
        continue;
      }

      // 列表内的缩进延续行：属于当前列表块，不新建块
      if (inList && /^\s+\S/.test(raw)) {
        consecutiveEmptyCount = 0;
        continue;
      }

      // 顶格的非列表行 → 列表结束
      inList = false;

      // 标题
      if (/^#{1,6}\s/.test(trimmed)) {
        blockStarts.push(i);
        pendingBlock = false;
        consecutiveEmptyCount = 0;
        continue;
      }

      // 水平线
      if (/^\s{0,3}(-{3,}|\*{3,}|_{3,})\s*$/.test(trimmed)) {
        blockStarts.push(i);
        pendingBlock = false;
        consecutiveEmptyCount = 0;
        continue;
      }

      // 块引用
      if (trimmed.startsWith('>')) {
        if (!pendingBlock) {
          blockStarts.push(i);
        }
        pendingBlock = true;
        consecutiveEmptyCount = 0;
        continue;
      }

      // 普通文本行（段落）：只有在新段落开始时记录行号
      if (!pendingBlock) {
        blockStarts.push(i);
      }
      pendingBlock = true;
      consecutiveEmptyCount = 0;
    }
    
    // 2. 解析渲染好的 HTML，收集顶级块级元素（保持顺序）
    // 只收集 container 的直接子元素中的块级元素，不递归进入块内部
    // 因为 markdown 中一个块（如列表、表格）对应一个 HTML 块元素
    const parser = new DOMParser();
    const doc = parser.parseFromString(`<div>${html}</div>`, "text/html");
    const container = doc.querySelector('div') || doc.body;
    const htmlBlocks = [];
    
    for (let child = container.firstElementChild; child; child = child.nextElementSibling) {
      const tag = child.tagName.toLowerCase();
      if (['h1','h2','h3','h4','h5','h6','p','pre','blockquote','hr','table','ul','ol','div'].includes(tag)) {
        htmlBlocks.push(child);
      }
    }
    
    // 3. 按顺序映射行号：第 i 个 HTML 块对应对应第 i 个 markdown 块的行号
    let blockIdx = 0;
    for (const el of htmlBlocks) {
      if (blockIdx < blockStarts.length) {
        const lineNum = blockStarts[blockIdx] + 1; // 转为 1-based
        el.setAttribute('data-line', lineNum.toString());
        blockIdx++;
      }
    }
    
    return doc.documentElement.innerHTML;
  } catch (e) {
    return html;
  }
}

function getLineNumberAtCursor() {
  const pos = el.editor.selectionStart;
  const text = el.editor.value;
  return text.substring(0, pos).split(/\r?\n/).length;
}

let _syncScrolling = false; // 防止互相触发循环

function syncScrollOnEdit() {
  const lineNum = getLineNumberAtCursor();
  syncPreviewToLine(lineNum);
  highlightPreviewLine(lineNum);
}

function syncPreviewToLine(lineNum) {
  if (_syncScrolling || el.preview.hidden || el.preview.offsetHeight === 0) return;
  
  // 找到最接近但不超过 lineNum 的 data-line 元素（语义匹配）
  // 这样当光标在多行段落的中间行时，仍然能正确匹配到该段落的起始块
  let targetElement = null;
  let bestLine = -1;
  
  const allLineElements = el.preview.querySelectorAll('[data-line]');
  for (const elem of allLineElements) {
    const dl = parseInt(elem.getAttribute('data-line'), 10);
    if (dl <= lineNum && dl > bestLine) {
      bestLine = dl;
      targetElement = elem;
    }
  }
  
  // 如果没有 < 的行，取最近的大于行
  if (!targetElement) {
    let bestDiff = Infinity;
    for (const elem of allLineElements) {
      const dl = parseInt(elem.getAttribute('data-line'), 10);
      const diff = Math.abs(dl - lineNum);
      if (diff < bestDiff) {
        bestDiff = diff;
        targetElement = elem;
      }
    }
  }
  
  if (targetElement) {
    const previewRect = el.preview.getBoundingClientRect();
    const rect = targetElement.getBoundingClientRect();
    
    // 检查元素是否在可见区域之外
    const isAbove = rect.top < previewRect.top;
    const isBelow = rect.bottom > previewRect.bottom;
    
    if (isAbove || isBelow) {
      _syncScrolling = true;
      // 使用更精确的滚动方式
      const offsetTop = targetElement.offsetTop - el.preview.offsetHeight * 0.3;
      el.preview.scrollTop = Math.max(0, offsetTop);
      _syncScrolling = false;
    }
  }
}

function findBestMatchLine(lineNum) {
  // 语义匹配：找到 data-line <= lineNum 中的最大值
  let bestLine = -1;
  let bestElement = null;
  
  const allLineElements = el.preview.querySelectorAll('[data-line]');
  for (const elem of allLineElements) {
    const dl = parseInt(elem.getAttribute('data-line'), 10);
    if (dl <= lineNum && dl > bestLine) {
      bestLine = dl;
      bestElement = elem;
    }
  }
  
  // 如果没有小于等于的，取最近的
  if (!bestElement) {
    let bestDiff = Infinity;
    for (const elem of allLineElements) {
      const dl = parseInt(elem.getAttribute('data-line'), 10);
      const diff = Math.abs(dl - lineNum);
      if (diff < bestDiff) {
        bestDiff = diff;
        bestElement = elem;
      }
    }
  }
  
  return bestElement ? parseInt(bestElement.getAttribute('data-line'), 10) : null;
}

function highlightPreviewLine(lineNum) {
  // 清除旧的激活高亮
  el.preview.querySelectorAll('.sync-active-line').forEach(el => el.classList.remove('sync-active-line'));
  // 语义匹配找到最合适的行
  const bestLine = findBestMatchLine(lineNum);
  if (bestLine) {
    const targetElement = el.preview.querySelector(`[data-line="${bestLine}"]`);
    if (targetElement) {
      targetElement.classList.add('sync-active-line');
    }
  }
}

function setupSyncScroll() {
  // 移除旧的事件监听器
  el.editor.removeEventListener("click", handleEditorClick);
  el.preview.removeEventListener("click", handlePreviewClick);
  el.editor.removeEventListener("keyup", handleEditorKeyup);
  el.preview.removeEventListener("scroll", handlePreviewScroll);
  
  // 添加新的事件监听器
  el.editor.addEventListener("click", handleEditorClick);
  el.preview.addEventListener("click", handlePreviewClick);
  el.editor.addEventListener("keyup", handleEditorKeyup);
  el.preview.addEventListener("scroll", handlePreviewScroll, { passive: true });
}

function handleEditorKeyup(e) {
  // 方向键、PageUp/Down、Home/End 等导航键才触发同步
  const navKeys = ["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "PageUp", "PageDown", "Home", "End"];
  if (navKeys.includes(e.key)) {
    const lineNum = getLineNumberAtCursor();
    syncPreviewToLine(lineNum);
    highlightPreviewLine(lineNum);
  }
}

function handleEditorClick(e) {
  const lineNum = getLineNumberAtCursor();
  syncPreviewToLine(lineNum);
  highlightPreviewLine(lineNum);
}

function handlePreviewClick(e) {
  // 查找点击元素或其父元素中的 data-line 属性
  let target = e.target;
  let lineNum = null;
  
  while (target && target !== el.preview) {
    const dataLine = target.getAttribute("data-line");
    if (dataLine) {
      lineNum = parseInt(dataLine, 10);
      break;
    }
    target = target.parentElement;
  }
  
  if (lineNum) {
    // 在编辑器中定位到相应行
    scrollEditorToLine(lineNum);
  }
}

function handlePreviewScroll() {
  if (_syncScrolling || el.editor.hidden) return;
  
  // 获取预览中当前可见区域的第一个块级元素及其行号
  const previewRect = el.preview.getBoundingClientRect();
  const previewTop = previewRect.top + 10; // 加一点偏移取可见区域稍下的位置
  
  // 遍历所有有 data-line 的元素，找到第一个在可见区域内的
  const lineElements = el.preview.querySelectorAll('[data-line]');
  let bestLine = null;
  let bestDistance = Infinity;
  
  for (const elem of lineElements) {
    const rect = elem.getBoundingClientRect();
    const distance = Math.abs(rect.top - previewTop);
    if (rect.top < previewRect.bottom && rect.bottom > previewRect.top && distance < bestDistance) {
      bestDistance = distance;
      bestLine = parseInt(elem.getAttribute('data-line'), 10);
    }
  }
  
  if (bestLine) {
    // 找到该区块起始行的最近匹配行（语义匹配）
    const matchLine = findBestMatchLine(bestLine) || bestLine;
    scrollEditorToLine(matchLine, true);
  }
}

function scrollEditorToLine(lineNum, silent = false) {
  if (_syncScrolling) return;
  const lines = el.editor.value.split(/\r?\n/);
  let pos = 0;
  
  // 计算到达指定行的字符位置
  for (let i = 0; i < Math.min(lineNum - 1, lines.length); i++) {
    pos += lines[i].length + 1; // +1 for newline
  }
  
  el.editor.setSelectionRange(pos, pos);
  if (!silent) el.editor.focus();

  // 在编辑器中滚动到该行（计入文本域的上内边距，避免定位偏上）
  const styles = window.getComputedStyle(el.editor);
  const lineHeight = parseFloat(styles.lineHeight) || 22;
  const paddingTop = parseFloat(styles.paddingTop) || 0;
  const cursorLine = lineNum - 1;
  const scrollTop = Math.max(0, cursorLine * lineHeight + paddingTop - el.editor.clientHeight / 3);
  el.editor.scrollTop = scrollTop;
  
  highlightPreviewLine(lineNum);
}

function setViewMode(mode) {
  const wrapper = el.editor.closest(".split");
  if (!wrapper) return;
  
  if (mode === "fullscreen") {
    // 进入全屏
    state.fullscreen = true;
    document.body.classList.add("fullscreen-mode");
    wrapper.dataset.viewMode = "split";
    el.editor.hidden = false;
    el.preview.hidden = false;
    el.editor.focus();
  } else if (mode === "exit-fullscreen") {
    // 退出全屏
    state.fullscreen = false;
    document.body.classList.remove("fullscreen-mode");
    wrapper.dataset.viewMode = "split";
    el.editor.hidden = false;
    el.preview.hidden = false;
  } else if (state.fullscreen) {
    // 全屏模式下切换 分屏/仅源码/仅预览
    wrapper.dataset.viewMode = mode;
    el.editor.hidden = mode === "preview";
    el.preview.hidden = mode === "source";
    if (mode !== "preview") el.editor.focus();
  } else {
    // 普通模式
    state.fullscreen = false;
    document.body.classList.remove("fullscreen-mode");
    wrapper.dataset.viewMode = mode;
    el.editor.hidden = mode === "preview";
    el.preview.hidden = mode === "source";
    if (mode !== "preview") el.editor.focus();
  }
  
  // 高亮当前按钮
  for (const btn of el.viewToggleButtons) {
    btn.classList.toggle("is-active", btn.dataset.viewMode === mode);
  }

  updateTocVisibility();
}

function markdownToHTML(markdown, withLineNumbers = false) {
  const lines = markdown.split(/\r?\n/);
  let html = "";
  let inCode = false;
  let inHTML = false;
  let rawHTML = [];
  let listOpen = false;
  for (let lineIdx = 0; lineIdx < lines.length; lineIdx++) {
    const raw = lines[lineIdx];
    const line = raw;
    const lineNum = lineIdx + 1;
    const dataLineAttr = withLineNumbers ? ` data-line="${lineNum}"` : "";
    
    if (line.trim() === "<!-- html:start -->") {
      if (listOpen) {
        html += "</ul>";
        listOpen = false;
      }
      inHTML = true;
      rawHTML = [];
      continue;
    }
    if (line.trim() === "<!-- html:end -->") {
      html += `<div class="embedded-html"${dataLineAttr}>${rawHTML.join("\n")}</div>`;
      inHTML = false;
      continue;
    }
    if (inHTML) {
      rawHTML.push(line);
      continue;
    }
    if (line.startsWith("```")) {
      if (inCode) {
        html += "</code></pre>";
        inCode = false;
      } else {
        html += `<pre${dataLineAttr}><code>`;
        inCode = true;
      }
      continue;
    }
    if (inCode) {
      html += escapeHTML(line) + "\n";
      continue;
    }
    if (/^\s*<(img|iframe|video|div|table|details|canvas|svg|audio)\b/i.test(line)) {
      html += line;
      continue;
    }
    const listMatch = line.match(/^\s*[-*]\s+(.+)/);
    if (listMatch) {
      if (!listOpen) {
        html += `<ul${dataLineAttr}>`;
        listOpen = true;
      }
      html += `<li>${inlineMarkdown(listMatch[1])}</li>`;
      continue;
    }
    if (listOpen) {
      html += "</ul>";
      listOpen = false;
    }
    if (/^\s{0,3}(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      html += `<hr${dataLineAttr}>`;
      continue;
    }
    if (/^###\s+/.test(line)) html += `<h3${dataLineAttr}>${inlineMarkdown(line.replace(/^###\s+/, ""))}</h3>`;
    else if (/^##\s+/.test(line)) html += `<h2${dataLineAttr}>${inlineMarkdown(line.replace(/^##\s+/, ""))}</h2>`;
    else if (/^#\s+/.test(line)) html += `<h1${dataLineAttr}>${inlineMarkdown(line.replace(/^#\s+/, ""))}</h1>`;
    else if (line.trim() === "") html += `<br${dataLineAttr}>`;
    else html += `<p${dataLineAttr}>${inlineMarkdown(line)}</p>`;
  }
  if (listOpen) html += "</ul>";
  if (inCode) html += "</code></pre>";
  return html;
}

function inlineMarkdown(text) {
  let out = escapeHTML(text);
  out = out.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, '<img alt="$1" src="$2" width="480">');
  out = out.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noreferrer">$1</a>');
  out = out.replace(/`([^`]+)`/g, "<code>$1</code>");
  out = out.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  out = out.replace(/\*([^*]+)\*/g, "<em>$1</em>");
  return out;
}

function imageHTML(src, alt, width) {
  return `<img src="${escapeAttr(src)}" alt="${escapeAttr(alt)}" width="${safeSize(width, 480)}">`;
}

function enhanceResizableMedia() {
  for (const media of [...el.preview.querySelectorAll("img, iframe, video")]) {
    if (media.closest(".resizable-media")) continue;
    const wrapper = document.createElement("span");
    wrapper.className = "resizable-media";
    wrapper.dataset.src = media.dataset.noteSrc || media.getAttribute("src") || "";
    media.parentNode.insertBefore(wrapper, media);
    wrapper.appendChild(media);
    const handle = document.createElement("span");
    handle.className = "resize-handle";
    handle.title = "拖拽调整尺寸";
    wrapper.appendChild(handle);
    attachResizeHandle(wrapper, media, handle);
  }
}

function attachResizeHandle(wrapper, media, handle) {
  handle.addEventListener("mousedown", event => {
    event.preventDefault();
    const startX = event.clientX;
    const startY = event.clientY;
    const rect = media.getBoundingClientRect();
    const startW = rect.width;
    const startH = rect.height;
    const ratio = startH / startW || 0.5625;
    const move = moveEvent => {
      const nextW = Math.max(120, Math.round(startW + moveEvent.clientX - startX));
      const nextH = media.tagName === "IMG"
        ? Math.round(nextW * ratio)
        : Math.max(90, Math.round(startH + moveEvent.clientY - startY));
      media.style.width = `${nextW}px`;
      media.style.height = `${nextH}px`;
      wrapper.dataset.width = String(nextW);
      wrapper.dataset.height = String(nextH);
    };
    const up = () => {
      document.removeEventListener("mousemove", move);
      document.removeEventListener("mouseup", up);
      const width = parseInt(wrapper.dataset.width || String(Math.round(startW)), 10);
      const height = parseInt(wrapper.dataset.height || String(Math.round(startH)), 10);
      updateMediaSize(wrapper.dataset.src, media.tagName.toLowerCase(), width, height);
      state.dirty = true;
      renderPreview();
      scheduleAutoSave();
      setStatus("已更新媒体尺寸，正在自动保存");
    };
    document.addEventListener("mousemove", move);
    document.addEventListener("mouseup", up);
  });
}

function updateMediaSize(src, tag, width, height) {
  const rawSrc = stripBasePath(src).replace(/^\/files\/[^/]+\/img\//, "../img/");
  const escaped = escapeRegExp(rawSrc);
  const htmlTagRe = new RegExp(`<${tag}\\b(?=[^>]*\\bsrc=["']${escaped}["'])[^>]*>`, "i");
  if (htmlTagRe.test(el.editor.value)) {
    el.editor.value = el.editor.value.replace(htmlTagRe, match => setMediaAttrs(match, width, height));
    return;
  }
  if (tag === "img") {
    const mdImageRe = new RegExp(`!\\[([^\\]]*)\\]\\(${escaped}\\)`);
    if (mdImageRe.test(el.editor.value)) {
      el.editor.value = el.editor.value.replace(mdImageRe, (_, alt) => imageHTML(rawSrc, alt || rawSrc.split("/").pop(), width));
      return;
    }
  }
  const broadTagRe = new RegExp(`<${tag}\\b(?=[^>]*\\bsrc=["'][^"']*${escapeRegExp(rawSrc.split("/").pop())}["'])[^>]*>`, "i");
  el.editor.value = el.editor.value.replace(broadTagRe, match => setMediaAttrs(match, width, height));
}

function setMediaAttrs(tagText, width, height) {
  let next = tagText;
  if (/\bwidth=["'][^"']*["']/i.test(next)) next = next.replace(/\bwidth=["'][^"']*["']/i, `width="${safeSize(width, 480)}"`);
  else next = next.replace(/>$/, ` width="${safeSize(width, 480)}">`);
  if (/\bheight=["'][^"']*["']/i.test(next)) next = next.replace(/\bheight=["'][^"']*["']/i, `height="${safeSize(height, 270)}"`);
  else if (!/^<img\b/i.test(next)) next = next.replace(/>$/, ` height="${safeSize(height, 270)}">`);
  return next;
}

function normalizeVideoURL(url) {
  const yt = url.match(/(?:youtube\.com\/watch\?v=|youtu\.be\/)([A-Za-z0-9_-]+)/);
  if (yt) return `https://www.youtube.com/embed/${yt[1]}`;
  const bilibili = url.match(/bilibili\.com\/video\/([^/?#]+)/);
  if (bilibili) return `https://player.bilibili.com/player.html?bvid=${bilibili[1]}`;
  return url;
}

async function api(url, options = {}) {
  const opts = { ...options };
  const expectsJSON = opts.json !== false;
  delete opts.json;
  opts.cache = "no-store";
  opts.headers = opts.headers || {};
  if (opts.body && typeof opts.body === "string") {
    opts.headers["Content-Type"] = "application/json";
  }
  const res = await fetch(appURL(url), opts);
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || res.statusText);
  }
  return expectsJSON ? res.json() : res.json();
}

function appURL(path) {
  if (/^https?:\/\//i.test(path)) return path;
  if (!path.startsWith("/")) path = "/" + path;
  return `${basePath}${path}`;
}

function stripBasePath(path) {
  if (!basePath || !path.startsWith(basePath + "/")) return path;
  return path.slice(basePath.length);
}

function normalizeMarkdownName(name) {
  name = (name || "").trim();
  if (!name) return "";
  name = name.replace(/\.markdown$/i, "");
  if (!/\.md$/i.test(name)) name += ".md";
  return name;
}

function isMarkdownFile(name) {
  return /\.(md|markdown)$/i.test(name || "");
}

function escapeHTML(value) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function escapeAttr(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll('"', "&quot;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}

function escapeRegExp(value) {
  return String(value).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function safeSize(value, fallback) {
  const n = Number.parseInt(value, 10);
  if (!Number.isFinite(n)) return fallback;
  return Math.min(2400, Math.max(40, n));
}

function setStatus(text) {
  el.status.textContent = text;
}
