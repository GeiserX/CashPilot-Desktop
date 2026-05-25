# PyInstaller spec for CashPilot sidecar (onedir mode)
# Build: pyinstaller cashpilot-sidecar.spec
# Output: dist/cashpilot-sidecar/ (directory with executable + libs)

import os
import sys

block_cipher = None
backend_path = os.environ.get('CASHPILOT_BACKEND_PATH', '../CashPilot')

a = Analysis(
    ['desktop_main.py'],
    pathex=[backend_path],
    binaries=[],
    datas=[
        (os.path.join(backend_path, 'services'), 'services'),
        (os.path.join(backend_path, 'app/templates'), 'app/templates'),
        (os.path.join(backend_path, 'app/static'), 'app/static'),
    ],
    hiddenimports=[
        'app.main',
        'app.worker_api',
        'app.orchestrator',
        'app.database',
        'app.compose_generator',
        'app.constants',
        'app.collectors',
        'app.collectors.anyone',
        'uvicorn',
        'uvicorn.logging',
        'uvicorn.loops',
        'uvicorn.loops.auto',
        'uvicorn.protocols',
        'uvicorn.protocols.http',
        'uvicorn.protocols.http.auto',
        'uvicorn.protocols.websockets',
        'uvicorn.protocols.websockets.auto',
        'uvicorn.lifespan',
        'uvicorn.lifespan.on',
        'fastapi',
        'starlette',
        'httpx',
        'aiosqlite',
        'docker',
        'cryptography',
        'apscheduler',
        'jinja2',
        'yaml',
    ],
    hookspath=[],
    hooksconfig={},
    runtime_hooks=[],
    excludes=[],
    win_no_prefer_redirects=False,
    win_private_assemblies=False,
    cipher=block_cipher,
    noarchive=False,
)

pyz = PYZ(a.pure, a.zipped_data, cipher=block_cipher)

exe = EXE(
    pyz,
    a.scripts,
    [],
    exclude_binaries=True,
    name='cashpilot-sidecar',
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=True,
    console=False,
    disable_windowed_traceback=False,
    argv_emulation=False,
    target_arch=None,
    codesign_identity=None,
    entitlements_file=None,
)

coll = COLLECT(
    exe,
    a.binaries,
    a.zipfiles,
    a.datas,
    strip=False,
    upx=True,
    upx_exclude=[],
    name='cashpilot-sidecar',
)
