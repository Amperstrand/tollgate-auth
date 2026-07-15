"""NIP-44 v2 encryption + NIP-59 gift wrapping for ContextVM.

Self-contained implementation using the `cryptography` library.
No external Nostr deps beyond pynostr for event signing.
"""

import base64
import hashlib
import hmac
import os
import struct
import time
from math import floor, log2

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives.kdf.hkdf import HKDF, HKDFExpand
from cryptography.hazmat.backends import default_backend


# ─── secp256k1 ECDH ───

def _ecdh_x(priv_hex: str, pub_hex: str) -> bytes:
    """Compute ECDH shared x-coordinate (32 bytes, unhashed)."""
    priv_int = int(priv_hex, 16)
    priv_key = ec.derive_private_key(priv_int, ec.SECP256K1(), default_backend())

    pub_bytes = bytes.fromhex(pub_hex)
    # Try even y (0x02) first, fall back to odd y (0x03)
    for prefix in (b"\x02", b"\x03"):
        try:
            compressed = prefix + pub_bytes
            pub_key = ec.EllipticCurvePublicKey.from_encoded_point(
                ec.SECP256K1(), compressed
            )
            # Verify the point is valid by testing exchange
            return priv_key.exchange(ec.ECDH(), pub_key)
        except Exception:
            continue
    raise ValueError(f"Invalid public key: {pub_hex}")


# ─── HKDF ───

def _hkdf_extract(ikm: bytes, salt: bytes) -> bytes:
    return hmac.new(salt, ikm, hashlib.sha256).digest()


def _hkdf_expand(prk: bytes, info: bytes, length: int) -> bytes:
    n = (length + 31) // 32
    okm = b""
    t = b""
    for i in range(1, n + 1):
        t = hmac.new(prk, t + info + bytes([i]), hashlib.sha256).digest()
        okm += t
    return okm[:length]


# ─── HMAC-SHA256 ───

def _hmac_sha256(key: bytes, message: bytes) -> bytes:
    return hmac.new(key, message, hashlib.sha256).digest()


# ─── ChaCha20 (no AEAD, RFC 8439 §2.4) ───

def _chacha20(key: bytes, nonce: bytes, data: bytes) -> bytes:
    cipher = Cipher(
        algorithms.ChaCha20(key, b"\x00\x00\x00\x00" + nonce),
        mode=None,
        backend=default_backend(),
    )
    enc = cipher.encryptor()
    return enc.update(data) + enc.finalize()


# ─── Padding ───

def _calc_padded_len(unpadded_len: int) -> int:
    if unpadded_len <= 32:
        return 32
    next_power = 1 << (floor(log2(unpadded_len - 1)) + 1)
    chunk = 32 if next_power <= 256 else next_power // 8
    return chunk * (floor((unpadded_len - 1) / chunk) + 1)


def _pad(plaintext: bytes) -> bytes:
    unpadded_len = len(plaintext)
    if unpadded_len < 1 or unpadded_len > 65535:
        raise ValueError("invalid plaintext length")
    prefix = struct.pack(">H", unpadded_len)
    padded_len = _calc_padded_len(unpadded_len)
    suffix = b"\x00" * (padded_len - unpadded_len)
    return prefix + plaintext + suffix


def _unpad(padded: bytes) -> bytes:
    unpadded_len = struct.unpack(">H", padded[0:2])[0]
    unpadded = padded[2:2 + unpadded_len]
    if unpadded_len == 0 or len(unpadded) != unpadded_len:
        raise ValueError("invalid padding")
    return unpadded


# ─── Conversation key ───

def get_conversation_key(priv_hex: str, pub_hex: str) -> bytes:
    """Compute the long-term conversation key (32 bytes)."""
    shared_x = _ecdh_x(priv_hex, pub_hex)
    salt = b"nip44-v2"
    return _hkdf_extract(shared_x, salt)


# ─── Message keys ───

def _get_message_keys(conversation_key: bytes, nonce: bytes):
    keys = _hkdf_expand(conversation_key, nonce, 76)
    return keys[0:32], keys[32:44], keys[44:76]


# ─── Encrypt / Decrypt ───

def encrypt(plaintext: str, conversation_key: bytes, nonce: bytes = None) -> str:
    if nonce is None:
        nonce = os.urandom(32)
    chacha_key, chacha_nonce, hmac_key = _get_message_keys(conversation_key, nonce)
    padded = _pad(plaintext.encode("utf-8"))
    ciphertext = _chacha20(chacha_key, chacha_nonce, padded)
    mac = _hmac_sha256(hmac_key, nonce + ciphertext)
    payload = bytes([2]) + nonce + ciphertext + mac
    return base64.b64encode(payload).decode("ascii")


def decrypt(payload_b64: str, conversation_key: bytes) -> str:
    data = base64.b64decode(payload_b64)
    version = data[0]
    if version != 2:
        raise ValueError(f"unsupported version {version}")
    nonce = data[1:33]
    mac = data[-32:]
    ciphertext = data[33:-32]

    chacha_key, chacha_nonce, hmac_key = _get_message_keys(conversation_key, nonce)
    calculated_mac = _hmac_sha256(hmac_key, nonce + ciphertext)
    if not hmac.compare_digest(calculated_mac, mac):
        raise ValueError("invalid MAC")

    padded = _chacha20(chacha_key, chacha_nonce, ciphertext)
    return _unpad(padded).decode("utf-8")


# ─── Gift wrap (NIP-59) ───

EPHEMERAL_GIFT_WRAP_KIND = 21059
GIFT_WRAP_KIND = 1059
KIND_25910 = 25910


def create_gift_wrap(
    inner_content: str,
    recipient_pubkey_hex: str,
    sender_privkey_hex: str,
    kind: int = EPHEMERAL_GIFT_WRAP_KIND,
) -> dict:
    """Encrypt a message and wrap it in a gift-wrapped event.

    Returns a signed Nostr event dict ready to publish.
    """
    from pynostr.key import PrivateKey
    from pynostr.event import Event

    conv_key = get_conversation_key(sender_privkey_hex, recipient_pubkey_hex)
    encrypted = encrypt(inner_content, conv_key)

    event = Event(
        kind=kind,
        content=encrypted,
        tags=[["p", recipient_pubkey_hex]],
        created_at=int(time.time()),
    )

    gift_wrap_key = PrivateKey(bytes.fromhex(sender_privkey_hex))
    event.sign(gift_wrap_key.hex())
    return event.to_dict()


def is_gift_wrapped(event: dict) -> bool:
    return event.get("kind") in (GIFT_WRAP_KIND, EPHEMERAL_GIFT_WRAP_KIND)


def unwrap_gift(event: dict, server_privkey_hex: str) -> str:
    """Decrypt a gift-wrapped event's content."""
    sender_pubkey = event.get("pubkey", "")
    conv_key = get_conversation_key(server_privkey_hex, sender_pubkey)
    return decrypt(event.get("content", ""), conv_key)
