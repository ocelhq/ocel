#!/usr/bin/env python3
import base64
import ctypes
import ctypes.util
import os
import re
import sys

KEY_BYTES = 32
KEY_MODE = 0o400
NONCE_BYTES = 12
TAG_BYTES = 16

COORDINATE = ("project", "class", "env", "folder", "binding", "name")
SEAL_ROOT = "/etc/ocel"


def abort(said):
    sys.stderr.write("seal: " + said + "\n")
    sys.exit(2)


def crypto():
    for named in (ctypes.util.find_library("crypto"), "libcrypto.so.3", "libcrypto.so.1.1", "libcrypto.so"):
        if not named:
            continue
        try:
            return ctypes.CDLL(named)
        except OSError:
            continue
    abort("this host has no libcrypto")


def cipher(lib, key, nonce, body, aad, sealing):
    lib.EVP_CIPHER_CTX_new.restype = ctypes.c_void_p
    lib.EVP_aes_256_gcm.restype = ctypes.c_void_p
    ctx = lib.EVP_CIPHER_CTX_new()
    if not ctx:
        abort("libcrypto held no cipher context")
    try:
        init = lib.EVP_EncryptInit_ex if sealing else lib.EVP_DecryptInit_ex
        update = lib.EVP_EncryptUpdate if sealing else lib.EVP_DecryptUpdate
        if init(ctypes.c_void_p(ctx), ctypes.c_void_p(lib.EVP_aes_256_gcm()), None, key, nonce) != 1:
            abort("libcrypto refused an AES-256-GCM key of %d bytes" % len(key))
        moved = ctypes.c_int(0)
        if aad and update(ctypes.c_void_p(ctx), None, ctypes.byref(moved), aad, len(aad)) != 1:
            abort("libcrypto refused the coordinate")
        out = ctypes.create_string_buffer(len(body) + 16)
        if body and update(ctypes.c_void_p(ctx), out, ctypes.byref(moved), body, len(body)) != 1:
            abort("libcrypto refused the body")
        written = out.raw[: moved.value]
        return ctx, written
    except BaseException:
        lib.EVP_CIPHER_CTX_free(ctypes.c_void_p(ctx))
        raise


def seal(key, aad, plaintext):
    lib = crypto()
    nonce = os.urandom(NONCE_BYTES)
    ctx, body = cipher(lib, key, nonce, plaintext, aad, True)
    try:
        moved = ctypes.c_int(0)
        rest = ctypes.create_string_buffer(16)
        if lib.EVP_EncryptFinal_ex(ctypes.c_void_p(ctx), rest, ctypes.byref(moved)) != 1:
            abort("libcrypto would not finish sealing")
        body += rest.raw[: moved.value]
        tag = ctypes.create_string_buffer(TAG_BYTES)
        if lib.EVP_CIPHER_CTX_ctrl(ctypes.c_void_p(ctx), 0x10, TAG_BYTES, tag) != 1:
            abort("libcrypto returned no tag")
        return nonce + body + tag.raw
    finally:
        lib.EVP_CIPHER_CTX_free(ctypes.c_void_p(ctx))


def open_(key, aad, sealed):
    if len(sealed) < NONCE_BYTES + TAG_BYTES:
        abort("value is too short to be sealed")
    nonce = sealed[:NONCE_BYTES]
    tag = sealed[len(sealed) - TAG_BYTES :]
    body = sealed[NONCE_BYTES : len(sealed) - TAG_BYTES]
    lib = crypto()
    ctx, plaintext = cipher(lib, key, nonce, body, aad, False)
    try:
        if lib.EVP_CIPHER_CTX_ctrl(ctypes.c_void_p(ctx), 0x11, TAG_BYTES, tag) != 1:
            abort("libcrypto refused the tag")
        moved = ctypes.c_int(0)
        rest = ctypes.create_string_buffer(16)
        if lib.EVP_DecryptFinal_ex(ctypes.c_void_p(ctx), rest, ctypes.byref(moved)) != 1:
            abort("value was not sealed at this coordinate")
        return plaintext + rest.raw[: moved.value]
    finally:
        lib.EVP_CIPHER_CTX_free(ctypes.c_void_p(ctx))


def additional(at):
    return "".join(part.replace("%", "%25").replace("/", "%2F") + "/" for part in at).encode()


def coordinate(held, args):
    at = dict.fromkeys(COORDINATE, "")
    while args:
        flag = args.pop(0)
        field = flag[2:] if flag.startswith("--") else ""
        if field not in at or field == "class":
            abort("%s is not a coordinate flag" % flag)
        if not args:
            abort("%s was given no value" % flag)
        at[field] = args.pop(0)
    at["class"] = held
    return additional([at[field] for field in COORDINATE])


def key_of(path):
    try:
        with open(path, "rb") as f:
            held = f.read()
    except OSError as err:
        abort("cannot read key %s: %s" % (path, err))
    if len(held) != KEY_BYTES:
        abort("%s is %d bytes, want %d" % (path, len(held), KEY_BYTES))
    return held


def mint(path):
    if os.path.exists(path):
        abort("%s already exists" % path)
    staged = path + ".minting"
    try:
        os.unlink(staged)
    except FileNotFoundError:
        pass
    fd = os.open(staged, os.O_WRONLY | os.O_CREAT | os.O_EXCL, KEY_MODE)
    try:
        os.write(fd, os.urandom(KEY_BYTES))
    finally:
        os.close(fd)
    if os.geteuid() == 0:
        os.chown(staged, 0, 0)
    os.chmod(staged, KEY_MODE)
    try:
        os.link(staged, path)
    except FileExistsError:
        abort("%s was minted concurrently" % path)
    finally:
        os.unlink(staged)


def main(argv):
    if len(argv) < 2:
        abort("usage: seal <class> init|seal|open [coordinate flags]")
    held, verb, rest = argv[0], argv[1], list(argv[2:])
    if not re.fullmatch("[a-z0-9-]+", held):
        abort("%s is not a valid class" % held)
    path = os.path.join(SEAL_ROOT, held, "seal.key")

    if verb == "init":
        if rest:
            abort("init takes no coordinate")
        mint(path)
        return
    if verb not in ("seal", "open"):
        abort("unknown verb %s" % verb)

    aad = coordinate(held, rest)
    key = key_of(path)
    fed = sys.stdin.buffer.read().strip()
    try:
        body = base64.b64decode(fed, validate=True)
    except Exception:
        abort("stdin is not base64")
    written = seal(key, aad, body) if verb == "seal" else open_(key, aad, body)
    sys.stdout.write(base64.b64encode(written).decode() + "\n")


main(sys.argv[1:])
