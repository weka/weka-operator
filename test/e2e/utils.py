import sys
import time
from contextlib import contextmanager


def eventually(timeout: int = 10, interval: int = 1):
    success = False

    def succeed():
        nonlocal success
        success = True

    start = time.time()
    while time.time() - start < timeout:
        try:
            yield attempt(succeed)
        except AssertionError:
            time.sleep(interval)
            continue
        if success:
            break
    else:
        raise AssertionError("Timeout")


@contextmanager
def attempt(notify_success):
    try:
        yield
    except AssertionError:
        pass
    except Exception:
        raise
    else:
        notify_success()
