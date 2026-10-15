package bancoensaio

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// OS MOTORES DE OCR (AOS-529). Cada um é um adaptador de [MotorOCR], e tem um nome na linha de
// comandos. Um candidato novo entra como mais um adaptador; a experiência não muda.

// MotoresDeOCR são os nomes aceites por `aos-ensaio ocr --motor`.
var MotoresDeOCR = []string{NomeMotorTesseract, NomeMotorVisao, NomeMotorFalso}

const (
	NomeMotorTesseract = "tesseract"
	NomeMotorVisao     = "visao"
	NomeMotorFalso     = "falso"
)

// Os perfis do motor falso: o que lê tudo, o que perde os acentos, o que não lê PDF, e o que erra
// factos (troca zeros por «O» e corta um nome).
const (
	FalsoOCRCerto      = "certo"
	FalsoOCRSemAcentos = "sem-acentos"
	FalsoOCRFalhaPDF   = "falha-pdf"
	FalsoOCRErraFactos = "erra-factos"
)

// memoriaDoFalso é a memória que o motor falso declara: um número fixo, para o resumo ter uma
// coluna preenchida. Não mede nada.
const memoriaDoFalso = int64(64) << 20

// MotorOCRFalsoDoBanco é o motor falso: devolve o texto de origem da imagem, tal e qual, sem os
// acentos, com factos errados, ou falha nos PDF. Prova que o relatório distingue os quatro; não
// diz nada de um motor real.
type MotorOCRFalsoDoBanco struct{ Perfil string }

// NovoMotorOCRFalso devolve o motor falso do perfil, ou um erro se o perfil não existe.
func NovoMotorOCRFalso(perfil string) (*MotorOCRFalsoDoBanco, error) {
	switch perfil {
	case FalsoOCRCerto, FalsoOCRSemAcentos, FalsoOCRFalhaPDF, FalsoOCRErraFactos:
		return &MotorOCRFalsoDoBanco{Perfil: perfil}, nil
	}
	return nil, fmt.Errorf("banco-ensaio: perfil do motor de OCR falso desconhecido: %q (aceites: %s, %s, %s, %s)", perfil, FalsoOCRCerto, FalsoOCRSemAcentos, FalsoOCRFalhaPDF, FalsoOCRErraFactos)
}

// Nome implementa [MotorOCR].
func (f *MotorOCRFalsoDoBanco) Nome() string { return NomeMotorFalso + "-" + f.Perfil }

// Descricao implementa [MotorOCR].
func (f *MotorOCRFalsoDoBanco) Descricao() string {
	return "motor falso do banco: devolve o texto de origem; nao mede um motor real"
}

// Condicoes implementa [MotorOCR].
func (f *MotorOCRFalsoDoBanco) Condicoes() CondicoesDoMotor {
	return CondicoesDoMotor{Limites: limitesDoGVisor, Notas: []string{"motor falso: os numeros provam que o relatorio distingue, nao medem um motor"}}
}

// Ler implementa [MotorOCR].
func (f *MotorOCRFalsoDoBanco) Ler(_ context.Context, m MediaOCR) (LeituraOCR, error) {
	texto := m.textoDeOrigem
	switch f.Perfil {
	case FalsoOCRSemAcentos:
		texto = tirarAcentos(texto)
	case FalsoOCRErraFactos:
		texto = errarFactos(texto)
	case FalsoOCRFalhaPDF:
		if m.Formato == FormatoPDF {
			return LeituraOCR{}, errors.New("o motor falso nao le PDF")
		}
	}
	return LeituraOCR{Texto: texto, MemoriaPico: memoriaDoFalso}, nil
}

// tirarAcentos leva cada letra acentuada à letra base, com a mesma caixa.
func tirarAcentos(s string) string {
	var b strings.Builder
	for _, r := range s {
		if base, ok := semAcento[unicode.ToLower(r)]; ok && unicode.IsLetter(base) {
			if unicode.IsUpper(r) {
				base = unicode.ToUpper(base)
			}
			r = base
		}
		b.WriteRune(r)
	}
	return b.String()
}

// errarFactos imita dois erros clássicos de OCR: na segunda linha (o código do documento) cada
// zero passa a «O»; a terceira linha (o nome) fica cortada a meio. O resto fica igual.
func errarFactos(s string) string {
	linhas := strings.Split(s, "\n")
	if len(linhas) > 1 {
		linhas[1] = strings.ReplaceAll(linhas[1], "0", "O")
	}
	if len(linhas) > 2 {
		r := []rune(linhas[2])
		linhas[2] = string(r[:len(r)/2])
	}
	return strings.Join(linhas, "\n")
}

// ─── Tesseract num contentor ───────────────────────────────────────────────────────────────

// LinguasDoTesseract são as línguas por omissão: português e inglês.
const LinguasDoTesseract = "por+eng"

// imagemFixada é uma imagem Docker fixada pelo conteúdo: `nome@sha256:<64 hex>` (sem etiqueta),
// ou o ID `sha256:<64 hex>` de uma imagem construída no posto, que não tem digest de registo.
var imagemFixada = regexp.MustCompile(`^(sha256:[0-9a-f]{64}|[a-z0-9][a-z0-9._/-]*@sha256:[0-9a-f]{64})$`)

// linguasValidas são línguas do Tesseract (`por`, `por+eng`).
var linguasValidas = regexp.MustCompile(`^[a-z_]{3,16}(\+[a-z_]{3,16})*$`)

// As marcas das linhas que o guião do contentor escreve no stderr.
const (
	marcaDaMemoria = "aos529-memoria-pico "
	marcaDoEstagio = "aos529-estagio "
)

// limiarDeMemoriaEsgotada é o pico (90% do limite) a partir do qual uma saída com erro conta como
// memória esgotada: o OOM killer actua no limite, e o pico fica lá.
const limiarDeMemoriaEsgotada = LimiteDeMemoriaDoOCR / 10 * 9

// guiaoDoTesseract corre DENTRO do contentor: lê a imagem e escreve o texto no stdout. Um PDF
// passa antes pelo `pdftoppm` a 300 ppp, pelo stdout (o Tesseract não lê PDF). O `sh` da Debian (o
// `dash`) não tem `pipefail`: o código de cada estágio do pipe sai pelo descritor 4 e é recolhido
// à parte, e o primeiro estágio que falhar é o que se diz. No stderr ficam o estágio que falhou e
// a memória máxima do cgroup do contentor. Não escreve em disco: o contentor é só de leitura.
const guiaoDoTesseract = `f="$1"; l="$2"; rc=0; st=""
case "$f" in
  *.pdf)
    exec 3>&1
    s=$( { { pdftoppm -r 300 -gray -png -singlefile "$f"; echo "p$?" >&4; } | { tesseract stdin stdout -l "$l" >&3; echo "t$?" >&4; }; } 4>&1 )
    for x in $s; do
      case "$x" in
        p0|t0) ;;
        p*) rc=${x#p}; st=pdftoppm ;;
        t*) if [ "$rc" = 0 ]; then rc=${x#t}; st=tesseract; fi ;;
      esac
    done ;;
  *) tesseract "$f" stdout -l "$l"; rc=$?; st=tesseract ;;
esac
p=$(cat /sys/fs/cgroup/memory.peak 2>/dev/null || cat /sys/fs/cgroup/memory/memory.max_usage_in_bytes 2>/dev/null || echo -)
[ "$rc" = 0 ] || echo "` + marcaDoEstagio + `$st $rc" >&2
echo "` + marcaDaMemoria + `$p" >&2
exit $rc`

// corredorDocker corre um comando `docker` e devolve o stdout, o stderr e o código de saída. Um
// erro só quando o processo nem chegou a dar código (o docker não existe, o contexto acabou).
type corredorDocker func(ctx context.Context, args ...string) (stdout, stderr []byte, codigo int, err error)

func correrDocker(ctx context.Context, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- argumentos do banco: a imagem fixada, nomes gerados e uma pasta temporaria; nada vem de um modelo
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return out.Bytes(), errb.Bytes(), ee.ExitCode(), nil
	}
	if err != nil {
		return out.Bytes(), errb.Bytes(), -1, err
	}
	return out.Bytes(), errb.Bytes(), 0, nil
}

// MotorOCRTesseract corre o Tesseract num contentor Docker fixado pelo conteúdo, com os limites do
// executor gVisor: sem rede, 45 s, 1 GiB, 512 processos, raiz só de leitura, sem capabilities,
// utilizador não-root, e a imagem montada só de leitura. O banco NÃO descarrega a imagem
// (`--pull never`). A imagem constrói-se com `ocr/Dockerfile`.
type MotorOCRTesseract struct {
	Imagem  string
	Linguas string
	versao  string
	correr  corredorDocker
	// limite é o tempo de uma leitura: [LimiteDeTempoDoOCR]; só os testes o encurtam.
	limite time.Duration
}

// ErrImagemDoMotor — a imagem do motor não está fixada pelo conteúdo, ou não está no posto.
var ErrImagemDoMotor = errors.New("banco-ensaio: imagem do motor de OCR invalida ou ausente")

// NovoMotorOCRTesseract valida a imagem (fixada pelo digest ou pelo ID), confirma que está no
// posto e lê a versão do Tesseract. `correr` nil ⇒ o Docker local.
func NovoMotorOCRTesseract(ctx context.Context, imagem, linguas string, correr corredorDocker) (*MotorOCRTesseract, error) {
	if !imagemFixada.MatchString(imagem) {
		return nil, fmt.Errorf("%w: %q nao esta fixada (nome@sha256:<64 hex>, sem etiqueta, ou o ID sha256:<64 hex>)", ErrImagemDoMotor, imagem)
	}
	if linguas == "" {
		linguas = LinguasDoTesseract
	}
	if !linguasValidas.MatchString(linguas) {
		return nil, fmt.Errorf("%w: linguas invalidas: %q", ErrImagemDoMotor, linguas)
	}
	if correr == nil {
		correr = correrDocker
	}
	if _, _, codigo, err := correr(ctx, "image", "inspect", "--format", "{{.Id}}", imagem); err != nil || codigo != 0 {
		return nil, fmt.Errorf("%w: a imagem %s nao esta no posto (o banco nao a descarrega; constroi-se com packages/qa/banco-ensaio/ocr/Dockerfile)", ErrImagemDoMotor, imagem)
	}
	out, errb, codigo, err := correr(ctx, "run", "--rm", "--pull", "never", "--network", "none", "--entrypoint", "tesseract", imagem, "--version")
	if err != nil || codigo != 0 {
		return nil, fmt.Errorf("%w: a imagem %s nao corre o tesseract", ErrImagemDoMotor, imagem)
	}
	versao := primeiraLinha(string(out))
	if versao == "" {
		versao = primeiraLinha(string(errb))
	}
	return &MotorOCRTesseract{Imagem: imagem, Linguas: linguas, versao: versao, correr: correr, limite: LimiteDeTempoDoOCR}, nil
}

func primeiraLinha(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(l)
}

// Nome implementa [MotorOCR].
func (t *MotorOCRTesseract) Nome() string { return NomeMotorTesseract }

// Descricao implementa [MotorOCR].
func (t *MotorOCRTesseract) Descricao() string {
	return fmt.Sprintf("%s; linguas %s; imagem %s", t.versao, t.Linguas, t.Imagem)
}

// Condicoes implementa [MotorOCR].
func (t *MotorOCRTesseract) Condicoes() CondicoesDoMotor {
	return CondicoesDoMotor{Limites: limitesDoGVisor, Notas: []string{
		"o contentor corre com o runc do Docker, NAO com o gVisor: os limites sao os do gVisor, o custo do gVisor (interposicao de syscalls) nao esta medido",
		"uma chamada de cada vez, isolada: em producao as chamadas podem ser concorrentes e partilham 1 GB e 512 processos do servico gvisor",
		"o tempo inclui o arranque do contentor, como no gVisor inclui o do runsc; a memoria e o pico do cgroup do contentor inteiro",
		"memoria esgotada = saida com erro e pico acima de 90% do limite; uma saida 137 sem pico medido conta como falha do motor",
	}}
}

// argumentosDoTesseract devolve os argumentos do `docker run` de uma leitura.
func argumentosDoTesseract(contentor, pasta, ficheiro, imagem, linguas string) []string {
	return []string{
		"run", "--rm", "--pull", "never", "--name", contentor,
		"--network", "none",
		"--memory", strconv.FormatInt(LimiteDeMemoriaDoOCR, 10), "--memory-swap", strconv.FormatInt(LimiteDeMemoriaDoOCR, 10),
		"--pids-limit", strconv.Itoa(LimiteDeProcessosOCR),
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--user", "65532:65532",
		"-v", pasta + ":/in:ro",
		"--entrypoint", "sh", imagem, "-c", guiaoDoTesseract, "aos529", "/in/" + ficheiro, linguas,
	}
}

// Ler implementa [MotorOCR]: escreve a imagem numa pasta temporária, monta-a só de leitura e
// corre o Tesseract com o limite de tempo. Passado o limite, o contentor é removido à força.
func (t *MotorOCRTesseract) Ler(ctx context.Context, m MediaOCR) (LeituraOCR, error) {
	pasta, err := os.MkdirTemp("", "aos529-ocr-")
	if err != nil {
		return LeituraOCR{}, err
	}
	defer os.RemoveAll(pasta)
	// A pasta nasce 0700; o contentor corre com o uid 65532, que num anfitrião Linux não a lia.
	if err := os.Chmod(pasta, 0o755); err != nil { // #nosec G302 -- pasta temporaria com uma imagem sintetica, lida pelo uid do contentor
		return LeituraOCR{}, err
	}
	if err := os.WriteFile(filepath.Join(pasta, m.Nome), m.Bytes, 0o644); err != nil { // #nosec G306 -- o contentor corre com outro uid e tem de ler a imagem sintetica
		return LeituraOCR{}, err
	}
	var sufixo [6]byte
	if _, err := rand.Read(sufixo[:]); err != nil {
		return LeituraOCR{}, err
	}
	contentor := prefixoDoDocker + "ocr-" + hex.EncodeToString(sufixo[:])
	limitado, cancelar := context.WithTimeout(ctx, t.limite)
	defer cancelar()
	out, errb, codigo, err := t.correr(limitado, argumentosDoTesseract(contentor, pasta, m.Nome, t.Imagem, t.Linguas)...)
	if limitado.Err() != nil {
		// O docker do lado de cá morreu com o contexto; o contentor pode continuar vivo.
		_, _, _, _ = t.correr(context.WithoutCancel(ctx), "rm", "-f", contentor)
		if ctx.Err() != nil {
			return LeituraOCR{}, ctx.Err()
		}
		return LeituraOCR{}, ErrOCRTempo
	}
	if err != nil {
		return LeituraOCR{}, err
	}
	pico := memoriaDoStderr(string(errb))
	if codigo != 0 {
		return LeituraOCR{}, classificarFalha(codigo, pico, estagioDoStderr(string(errb)))
	}
	return LeituraOCR{Texto: string(out), MemoriaPico: pico}, nil
}

// classificarFalha diz porque falhou uma leitura com saída diferente de zero: memória esgotada só
// com o pico do cgroup perto do limite, venha a falha de que estágio vier.
func classificarFalha(codigo int, pico int64, estagio string) error {
	if pico >= limiarDeMemoriaEsgotada {
		return fmt.Errorf("%w (%s saiu com %d, pico %d bytes)", ErrOCRMemoria, estagio, codigo, pico)
	}
	if estagio == "" {
		estagio = "contentor"
	}
	return fmt.Errorf("%s saiu com %d", estagio, codigo)
}

// memoriaDoStderr lê a memória máxima que o guião escreveu; -1 se não a escreveu ou não a soube.
func memoriaDoStderr(stderr string) int64 {
	for _, l := range strings.Split(stderr, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), marcaDaMemoria); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
				return n
			}
		}
	}
	return -1
}

// estagioDoStderr lê o estágio que falhou (`pdftoppm` ou `tesseract`); vazio se o guião não o disse.
func estagioDoStderr(stderr string) string {
	for _, l := range strings.Split(stderr, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), marcaDoEstagio); ok {
			nome, _, _ := strings.Cut(v, " ")
			if nome == "pdftoppm" || nome == "tesseract" {
				return nome
			}
		}
	}
	return ""
}
