# 🦊 SnowFox OS Console Launcher

A sleek, lightweight, and hardware-accelerated game launcher written in **Go (Golang)** using the **Gio UI** library. It is designed to act as a unified gaming hub for **SnowFoxOS**, blending perfectly with the system's consistent **SnowFox-Theme (v2.2)** design language.

In future releases of SnowFoxOS, this launcher will be pre-installed and natively integrated as a full console-mode gaming dashboard.

---

## 🚀 Feature Roadmap

| Feature / Platform | Steam | GOG | Epic Games | Retro Emulation | Custom Games |
| :--- | :---: | :---: | :---: | :---: | :---: |
| **Auto-Detect Installed Games** | ⚡ **Current** | 📅 Planned | 📅 Planned | 📅 Planned | 📅 Planned |
| **Launch from UI** | ⚡ **Current** | 📅 Planned | 📅 Planned | 📅 Planned | 📅 Planned |
| **Install & Manage Updates** | ❌ *Client Only* | 📅 Planned | 📅 Planned | ❌ *N/A* | ❌ *N/A* |
| **Uninstall Support** | ❌ *Client Only* | 📅 Planned | 📅 Planned | ❌ *N/A* | 📅 Planned |

### 🛠️ Important Clarification on Steam Integration
As reflected in the current Go codebase, **Steam games can only be scanned and launched**. Because Steam operates within its own highly protected client ecosystem, account management (such as installing new games, buying products, or removing apps) **must** be performed via the official Steam Client. 

For **GOG** and **Epic Games**, full management capabilities are planned—allowing you to browse, install, update, and completely manage games directly from inside this launcher interface.

---

## 🎨 Theme & Appearance

The application respects the official **SnowFox-Theme (v2.2)** configuration, relying on a unique, entirely custom-built dark palette:

* **Base Layer (`#11111b`)** – System background canvas.
* **Surface (`#1e1e2e`) & Cards (`#181825`)** – Structured containers for media layouts.
* **Secondary Accents (`#cba6f7`)** – Used for active UI states, categories, and focus loops.
* **Status Flags** – Green (`#a6e3a1`) for *Ready*, Amber (`#f9e2af`) for *Updates*, Red (`#f38ba8`) for *Alerts*.

It includes automatic library cache image scraping from `~/.steam/debian-installation/appcache/librarycache/` to seamlessly show high-quality game cover art inside your terminal grid.

---

## 🎵 Custom Background Music

The launcher utilizes `ffplay` to handle looped audio playback dynamically matching your UI lifecycle. 

* **Default Behavior:** Scans its immediate local executable directory for files named `music.mp3`, `music.ogg`, `music.flac`, `music.wav`, or `music.opus` on boot.
* **Customization:** Users can replace the default soundscape by dropping their own audio loops into a predefined folder, or explicitly loading them via flags.
* **Cinematic Fadeout:** When a game starts, the runner invokes an automated multi-step volume decrementing sequence before passing full execution over to your window manager workspace.

---

## ⌨️ Global Keybindings

| Hardware Input | Keyboard Emulation | Context: Main Grid | Context: Overlays (Launch / In-Game) |
| :--- | :--- | :--- | :--- |
| **D-Pad / Analogs** | `Up` / `Down` / `Left` / `Right` | Focus grid navigation | Move between options |
| **A Button** | `Return / Enter` | Open Launch Overlay | Confirm / Execute Choice |
| **LB / RB** | `Q` / `E` | Cycle through Platforms | *Ignored* |
| **Xbox / Guide** | `G` | *Ignored (unless game active)* | Open/Close Escape Menu |
| **Back** | `Escape` | Reset views | Cancel Overlay / Close |

---

## 💻 Invocation & Development

### Native OS Command
Once fully rolled out inside SnowFoxOS, you can bypass desktop environments and jump straight into full-screen console mode using the integrated hook:
```bash
snowfox node console
