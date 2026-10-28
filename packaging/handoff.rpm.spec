Name: handoff
Version: 1.0.8
Release: 1%{?dist}
Summary: Trusted peer-to-peer desktop file transfers
License: BSD-3-Clause AND LGPL-3.0-only AND GPL-2.0-only WITH Bootloader-exception
URL: https://github.com/tailscale/tailcat
BuildArch: x86_64
AutoReqProv: no
Requires: glibc >= 2.39, libstdc++, libgcc, fontconfig, freetype, libX11, libXext, libxcb, libxkbcommon, libxkbcommon-x11, libXcursor, libXi, libXrender, libXrandr, libwayland-client.so.0()(64bit), libwayland-cursor.so.0()(64bit), libwayland-egl.so.1()(64bit), mesa-libEGL, mesa-libGL, dbus-libs, zlib, xcb-util-cursor, xcb-util-image, xcb-util-keysyms, xcb-util-renderutil, xcb-util-wm
%description
handoff pairs independent computers and transfers files through tailcat's
encrypted data plane, with explicit approval and durable recovery.
Includes the Python and Qt runtime and the Go networking engine.
%prep
%build
%install
mkdir -p %{buildroot}/opt/handoff %{buildroot}%{_bindir} %{buildroot}%{_datadir}/applications %{buildroot}%{_datadir}/icons/hicolor/scalable/apps
cp -a %{bundle_dir}/. %{buildroot}/opt/handoff/
ln -s /opt/handoff/handoff %{buildroot}%{_bindir}/handoff
install -m644 %{source_dir}/packaging/handoff.desktop %{buildroot}%{_datadir}/applications/handoff.desktop
install -m644 %{source_dir}/assets/handoff.svg %{buildroot}%{_datadir}/icons/hicolor/scalable/apps/handoff.svg
%files
/opt/handoff
%{_bindir}/handoff
%{_datadir}/applications/handoff.desktop
%{_datadir}/icons/hicolor/scalable/apps/handoff.svg
%post
update-desktop-database >/dev/null 2>&1 || :
touch --no-create /usr/share/icons/hicolor >/dev/null 2>&1 || :
%postun
update-desktop-database >/dev/null 2>&1 || :
# Running applications and durable per-user data are intentionally preserved.
# Autostart points at the removed executable until disabled by the user.
