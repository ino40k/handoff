# Third-party components

handoff uses PySide6 / Qt 6 under LGPLv3, PyInstaller under GPLv2 with its
bootloader exception, and tailcat under BSD-3-Clause. Qt libraries are shipped
as separate, replaceable shared libraries in the frozen distribution. Source
and build instructions for this application are included in the repository.

* Qt / PySide6: https://code.qt.io/cgit/pyside/pyside-setup.git/ (tag v6.11.2)
* Qt source: https://download.qt.io/official_releases/qt/6.11/
* PyInstaller: https://github.com/pyinstaller/pyinstaller (v6.16.0)
* tailcat: https://github.com/tailscale/tailcat/tree/52fbad323e9d352f1febb623e0975368e9a8cbdd

Build scripts collect installed Python distribution license files and Go module
licenses into the distribution's licenses directory. Backend dependencies and
exact checksums are in backend/go.mod and backend/go.sum. `go version -m` on
the engine lists linked module versions. Consult the collected notices for
individual transitive licenses. Public tailcat DERP service terms and bandwidth
limits remain upstream's responsibility; handoff does not run a relay service.
