package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// hexDeTeste devolve 64 caracteres hex validos (uma seed ed25519).
func hexDeTeste() string {
	b := make([]byte, ed25519.SeedSize)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return hex.EncodeToString(b)
}

func ficheiro(t *testing.T, nome string, conteudo []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), nome)
	if err := os.WriteFile(p, conteudo, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// utf16le codifica como o `>` e o `Out-File` do PowerShell fazem: BOM + UTF-16 little-endian.
func utf16le(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// AS SEEDS NASCEM EM WINDOWS. Ver [limparSeedHex] para o porquê.
// ---------------------------------------------------------------------------------------------

// TestSeedComBOMCarrega — o caso que o `>` do PowerShell produz.
func TestSeedComBOMCarrega(t *testing.T) {
	p := ficheiro(t, "k.key", append([]byte{0xEF, 0xBB, 0xBF}, []byte(hexDeTeste()+"\r\n")...))
	if _, err := loadApproverKey(p); err != nil {
		t.Fatalf("uma seed com BOM nao carregou: %v\n"+
			"E o que o `openssl rand -hex 32 > ficheiro` produz em PowerShell, e o erro diria "+
			"«nao e hex» sem ninguem ligar isso a codificacao", err)
	}
}

// TestSeedDaISSUERKeyComBOMCarrega é a MUTAÇÃO DE CABLAGEM feita teste.
//
// `loadOrCreateKey` é um carregador SEPARADO, e é o da `issuer.key` — a chave que cunha TODAS as
// NHIs. Corrigir só o `loadApproverKey` deixava de fora a mais importante das três, e a bateria
// teria ficado verde na mesma. É a sexta vez que o padrão «unidade certa, cablagem errada»
// aparece neste repositório.
func TestSeedDaISSUERKeyComBOMCarrega(t *testing.T) {
	p := ficheiro(t, "issuer.key", append([]byte{0xEF, 0xBB, 0xBF}, []byte(hexDeTeste())...))
	if _, err := loadOrCreateKey(p); err != nil {
		t.Fatalf("a issuer.key com BOM nao carregou: %v", err)
	}
}

// TestSeedEmUTF16DizQueEUTF16 — o diagnóstico, que é metade do valor desta correcção.
func TestSeedEmUTF16DizQueEUTF16(t *testing.T) {
	comBom := ficheiro(t, "utf16bom.key", utf16le(hexDeTeste()))
	if _, err := loadApproverKey(comBom); !errors.Is(err, ErrSeedUTF16) {
		t.Errorf("UTF-16 COM BOM diagnosticado como %v — devia nomear a codificacao", err)
	}
	// SEM BOM é o caso mais comum do `Out-File`, e não tem prefixo que o denuncie: o sinal são
	// os NUL intercalados. Sem este ramo, cairia no «nao e hex» genérico.
	semBom := ficheiro(t, "utf16.key", utf16le(hexDeTeste())[2:])
	if _, err := loadApproverKey(semBom); !errors.Is(err, ErrSeedUTF16) {
		t.Errorf("UTF-16 SEM BOM diagnosticado como %v — e o caso mais comum do Out-File", err)
	}
}

// TestSeedInvalidaCONTINUAARecusar é o controlo que impede a limpeza de virar tolerância.
func TestSeedInvalidaCONTINUAARecusar(t *testing.T) {
	casos := map[string][]byte{
		"lixo":            []byte("isto nao e uma seed"),
		"hex_curto":       []byte("aabbcc"),
		"hex_com_lixo":    []byte("LIXO " + hexDeTeste()),
		"vazio":           {},
		"so_o_bom":        {0xEF, 0xBB, 0xBF},
		"bom_e_hex_curto": append([]byte{0xEF, 0xBB, 0xBF}, []byte("aabb")...),
	}
	for nome, conteudo := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := loadApproverKey(ficheiro(t, "k.key", conteudo)); err == nil {
				t.Errorf("%s foi ACEITE como seed — a limpeza do BOM virou tolerancia", nome)
			}
		})
	}
}

// TestASeedNuncaAparecNoErro — uma mensagem que ecoasse a chave seria pior do que o defeito.
func TestASeedNuncaAparecNoErro(t *testing.T) {
	h := hexDeTeste()
	// Seed com o comprimento errado: força a mensagem que FALA do conteúdo.
	_, err := loadApproverKey(ficheiro(t, "k.key", []byte(h+"aa")))
	if err == nil {
		t.Fatal("devia recusar")
	}
	if strings.Contains(err.Error(), h[:32]) {
		t.Errorf("o ERRO ecoa material da chave: %v", err)
	}
}
