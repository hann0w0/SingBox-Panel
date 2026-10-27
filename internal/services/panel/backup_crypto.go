package panel

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Encrypted backups wrap the ordinary tar.gz archive so it can be stored on a
// third-party drive. The archive contains jwt_secret and every node secret, so
// whoever reads a plaintext copy can forge an admin session.
//
// Layout:
//
//	header: magic(8) | salt(16) | argon2 time(4) | memory KiB(4) | threads(1) | nonce prefix(7)
//	body:   AES-256-GCM chunks of backupChunkSize plaintext bytes (+16 tag)
//
// Each chunk nonce is prefix(7) | counter(4) | final(1) and the whole header is
// the additional data, so reordering, truncation and header edits all fail
// authentication.
const (
	backupCryptoMagic      = "SBPBAK1\n"
	backupSaltSize         = 16
	backupNoncePrefixSize  = 7
	backupHeaderSize       = len(backupCryptoMagic) + backupSaltSize + 4 + 4 + 1 + backupNoncePrefixSize
	backupChunkSize        = 64 << 10
	backupTagSize          = 16
	minBackupPassphraseLen = 12
	maxBackupPassphraseLen = 1024

	// Bounds on header-supplied KDF parameters. A tampered header must not be
	// able to make the panel allocate gigabytes or spin for minutes.
	maxBackupKDFTime     = 10
	maxBackupKDFMemory   = 256 << 10 // KiB
	maxBackupKDFThreads  = 16
	minBackupKDFMemory   = 8 << 10 // KiB
	errBackupDecryptText = "备份口令错误或加密备份已损坏"
)

type backupKDFParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
}

// defaultBackupKDF is a variable so tests can use cheaper parameters; the
// parameters are stored in every header, so decryption never depends on it.
var defaultBackupKDF = backupKDFParams{Time: 3, Memory: 64 << 10, Threads: 4}

var errBackupPassphraseRequired = errors.New("此备份已加密，请提供备份口令")

func validateBackupPassphrase(passphrase string) error {
	if !utf8.ValidString(passphrase) {
		return errors.New("备份口令必须是有效的 UTF-8 文本")
	}
	n := utf8.RuneCountInString(passphrase)
	if n < minBackupPassphraseLen {
		return fmt.Errorf("备份口令至少需要 %d 个字符", minBackupPassphraseLen)
	}
	if len(passphrase) > maxBackupPassphraseLen {
		return fmt.Errorf("备份口令不能超过 %d 字节", maxBackupPassphraseLen)
	}
	return nil
}

func backupAEAD(passphrase string, salt []byte, params backupKDFParams) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(passphrase), salt, params.Time, params.Memory, params.Threads, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func backupChunkNonce(prefix []byte, counter uint32, final bool) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[backupNoncePrefixSize:], counter)
	if final {
		nonce[11] = 1
	}
	return nonce
}

// encryptBackup streams src into dst as an encrypted backup.
func encryptBackup(dst io.Writer, src io.Reader, passphrase string) error {
	if err := validateBackupPassphrase(passphrase); err != nil {
		return err
	}
	params := defaultBackupKDF
	header := make([]byte, backupHeaderSize)
	off := copy(header, backupCryptoMagic)
	salt := header[off : off+backupSaltSize]
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("生成备份加密盐失败：%w", err)
	}
	off += backupSaltSize
	binary.BigEndian.PutUint32(header[off:], params.Time)
	off += 4
	binary.BigEndian.PutUint32(header[off:], params.Memory)
	off += 4
	header[off] = params.Threads
	off++
	prefix := header[off : off+backupNoncePrefixSize]
	if _, err := io.ReadFull(rand.Reader, prefix); err != nil {
		return fmt.Errorf("生成备份加密随机数失败：%w", err)
	}

	aead, err := backupAEAD(passphrase, salt, params)
	if err != nil {
		return fmt.Errorf("创建备份加密器失败：%w", err)
	}
	if _, err := dst.Write(header); err != nil {
		return err
	}

	br := bufio.NewReaderSize(src, backupChunkSize)
	plain := make([]byte, backupChunkSize)
	sealed := make([]byte, 0, backupChunkSize+backupTagSize)
	for counter := uint32(0); ; counter++ {
		if counter == ^uint32(0) {
			return errors.New("备份过大，无法加密")
		}
		n, readErr := io.ReadFull(br, plain)
		final := false
		switch {
		case readErr == io.EOF || readErr == io.ErrUnexpectedEOF:
			final = true
		case readErr != nil:
			return readErr
		default:
			if _, peekErr := br.Peek(1); peekErr == io.EOF {
				final = true
			} else if peekErr != nil {
				return peekErr
			}
		}
		sealed = aead.Seal(sealed[:0], backupChunkNonce(prefix, counter, final), plain[:n], header)
		if _, err := dst.Write(sealed); err != nil {
			return err
		}
		if final {
			return nil
		}
	}
}

// isEncryptedBackup reports whether br starts with the encrypted-backup magic.
func isEncryptedBackup(br *bufio.Reader) bool {
	head, _ := br.Peek(len(backupCryptoMagic))
	return bytes.Equal(head, []byte(backupCryptoMagic))
}

// backupPlainReader returns a reader yielding the plain tar.gz archive. A
// plaintext archive passes through unchanged; an encrypted one requires a
// passphrase. Every chunk is authenticated before any of its bytes are released.
func backupPlainReader(src io.Reader, passphrase string) (io.Reader, bool, error) {
	br := bufio.NewReaderSize(src, backupChunkSize+backupTagSize+1)
	if !isEncryptedBackup(br) {
		return br, false, nil
	}
	if passphrase == "" {
		return nil, true, errBackupPassphraseRequired
	}
	header := make([]byte, backupHeaderSize)
	if _, err := io.ReadFull(br, header); err != nil {
		return nil, true, errors.New("加密备份头部不完整")
	}
	off := len(backupCryptoMagic)
	salt := header[off : off+backupSaltSize]
	off += backupSaltSize
	params := backupKDFParams{
		Time:    binary.BigEndian.Uint32(header[off:]),
		Memory:  binary.BigEndian.Uint32(header[off+4:]),
		Threads: header[off+8],
	}
	off += 9
	if params.Time < 1 || params.Time > maxBackupKDFTime ||
		params.Memory < minBackupKDFMemory || params.Memory > maxBackupKDFMemory ||
		params.Threads < 1 || params.Threads > maxBackupKDFThreads {
		return nil, true, errors.New("加密备份的密钥参数无效")
	}
	aead, err := backupAEAD(passphrase, salt, params)
	if err != nil {
		return nil, true, fmt.Errorf("创建备份解密器失败：%w", err)
	}
	return &backupDecryptReader{
		src:    br,
		aead:   aead,
		header: header,
		prefix: header[off : off+backupNoncePrefixSize],
		sealed: make([]byte, backupChunkSize+backupTagSize),
	}, true, nil
}

type backupDecryptReader struct {
	src     *bufio.Reader
	aead    cipher.AEAD
	header  []byte
	prefix  []byte
	counter uint32
	sealed  []byte
	plain   []byte
	done    bool
	err     error
}

func (r *backupDecryptReader) Read(p []byte) (int, error) {
	for len(r.plain) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		r.err = r.nextChunk()
	}
	n := copy(p, r.plain)
	r.plain = r.plain[n:]
	return n, nil
}

func (r *backupDecryptReader) nextChunk() error {
	n, err := io.ReadFull(r.src, r.sealed)
	final := false
	switch {
	case err == io.EOF:
		// A stream must end with an authenticated final chunk; running out of
		// input before one means the file was truncated at a chunk boundary.
		return errors.New(errBackupDecryptText)
	case err == io.ErrUnexpectedEOF:
		final = true
	case err != nil:
		return err
	default:
		if _, peekErr := r.src.Peek(1); peekErr == io.EOF {
			final = true
		} else if peekErr != nil {
			return peekErr
		}
	}
	if n < backupTagSize {
		return errors.New(errBackupDecryptText)
	}
	plain, openErr := r.aead.Open(r.sealed[:0], backupChunkNonce(r.prefix, r.counter, final), r.sealed[:n], r.header)
	if openErr != nil {
		return errors.New(errBackupDecryptText)
	}
	r.counter++
	r.plain = plain
	r.done = final
	return nil
}
