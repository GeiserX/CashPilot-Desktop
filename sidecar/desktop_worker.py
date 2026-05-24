import os
import uvicorn

from app.worker_api import app

if __name__ == "__main__":
    port = int(os.environ.get("UVICORN_PORT", "8081"))
    host = os.environ.get("CASHPILOT_HOST", "127.0.0.1")
    uvicorn.run(app, host=host, port=port)
