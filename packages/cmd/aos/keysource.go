package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// AOS-170 — KEYSOURCE PERSISTENTE do issuer. A chave de assinatura do issuer/operador
// TEM de vir de um substrato PERSISTENTE (config/vault/ficheiro), NÃO ser gerada por
// CSPRNG a cada arranque: uma chave nova a cada boot invalidaria TODOS os tokens
// emitidos antes do restart (o verifier deixaria de reconhecer a pubkey) — o oposto
// da durabilidade que AOS-170 exige. O seam já existe em [Config.IssuerSigningKey]
// (ed25519.PrivateKey vinda do chamador); este helper materializa a proveniência
// persistente de referência: um ficheiro de seed no disco, reutilizado entre
// reinícios. Em produção endurecida a chave vive num VAULT/HSM FORA do processo do
// nó (modo trust-anchor-only) — este ficheiro é a proveniência de REFERÊNCIA para o
// modo co-localizado, nunca um segredo hardcoded.
//
// Zero dependências externas (crypto/ed25519, crypto/rand, os).

// ErrBadIssuerKeyFile — o ficheiro de seed existe mas não contém uma seed ed25519
// válida (32 bytes). Fail-closed: uma seed malformada nunca compõe a autoridade.
var ErrBadIssuerKeyFile = errors.New("aos: ficheiro de seed do issuer invalido (esperado 32 bytes de seed ed25519)")

// LoadOrCreateIssuerKey devolve a chave de assinatura ed25519 do issuer a partir de
// um ficheiro de seed PERSISTENTE em path:
//
//   - se o ficheiro existe, lê a seed de 32 bytes e reconstrói a MESMA chave — os
//     tokens emitidos antes do restart continuam válidos (durabilidade);
//   - se não existe, gera uma seed nova por CSPRNG UMA vez, grava-a com permissões
//     0600 e devolve a chave. Nos arranques seguintes a MESMA chave é recarregada.
//
// A distinção crucial face ao arranque de referência de AOS-163 (que gera por CSPRNG
// a CADA boot): aqui a geração é uma vez só, e a chave é ESTÁVEL entre reinícios.
func LoadOrCreateIssuerKey(path string) (ed25519.PrivateKey, error) {
	seed, err := os.ReadFile(path)
	if err == nil {
		if len(seed) != ed25519.SeedSize {
			return nil, ErrBadIssuerKeyFile
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("aos: ler seed do issuer %q: %w", path, err)
	}

	// Primeira vez: gera uma seed e persiste-a (0600). A partir daqui é estável.
	fresh := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(rand.Reader, fresh); err != nil {
		return nil, fmt.Errorf("aos: gerar seed do issuer: %w", err)
	}
	// O_EXCL evita uma corrida que sobrescreva uma seed criada em paralelo.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// Outro arranque criou-a entretanto: recarrega a existente (estável).
			return LoadOrCreateIssuerKey(path)
		}
		return nil, fmt.Errorf("aos: criar seed do issuer %q: %w", path, err)
	}
	if _, err := f.Write(fresh); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("aos: gravar seed do issuer %q: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("aos: fsync seed do issuer %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	// DURABILIDADE: em POSIX a entrada de directório da seed RECÉM-CRIADA só é durável
	// após fsync do directório pai — sem isto, um crash logo após criar+gravar a seed
	// (já com f.Sync) poderia perder o ficheiro e forçar uma chave nova no próximo boot,
	// invalidando os tokens emitidos. Best-effort (plataformas sem fsync de directório
	// mantêm o conteúdo durável via f.Sync).
	if d, derr := os.Open(filepath.Dir(path)); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return ed25519.NewKeyFromSeed(fresh), nil
}
