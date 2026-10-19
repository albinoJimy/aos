package bancoensaio

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// O MP4 DA BATERIA DE VÍDEO (AOS-537). Os fotogramas desenha-os o banco, em Go; o MP4 gera-o o
// ffmpeg, SÓ NO BANCO (decisão do dono de 2026-10-18), num contentor Docker:
//
//   - a imagem fixada pelo digest ([ImagemDoFFmpeg]), sem etiqueta, e `--pull never` — o banco não
//     a descarrega; se não estiver no posto, recusa e diz o `docker pull` a fazer;
//   - sem rede, raiz só de leitura, sem capabilities, `no-new-privileges`, uid 65532, 1 GiB e 512
//     processos, e um limite de tempo por vídeo;
//   - os fotogramas entram por stdin, em RGBA cru;
//   - o MP4 (H.264, `yuv420p`, `+faststart`) sai para uma pasta temporária do posto, a ÚNICA coisa
//     montada, e o banco lê-o de lá e apaga a pasta. Não sai pelo stdout porque o `+faststart` —
//     o índice (`moov`) à cabeça do ficheiro — obriga o ffmpeg a reescrever o ficheiro no fim, e
//     um pipe não se reescreve: o muxer de MP4 recusa uma saída sem `seek`, e a alternativa (o MP4
//     fragmentado) não leva a duração na `mvhd`.
//
// O banco confere o MP4 que recebe (video_mp4.go) antes de o usar: `ftyp`, `moov` antes da `mdat`,
// a duração e as dimensões do vídeo da bateria, e um tamanho até [MaxBytesDoVideo].

// ImagemDoFFmpeg é a imagem do ffmpeg, fixada pelo digest (autorizada pelo dono e já no posto).
const ImagemDoFFmpeg = "jrottenberg/ffmpeg@sha256:8ec1ee1f6a0fcd37c97725827b6b7832795c9596e3439b8da56d7700d61ae778"

// Os limites de uma codificação.
const (
	LimiteDeTempoDoFFmpeg   = 120 * time.Second
	LimiteDeMemoriaDoFFmpeg = int64(1) << 30
	LimiteDeProcessosFFmpeg = 512
	// MaxBytesDoVideo é o maior MP4 que o banco manda ao modelo: os 4 MiB que o AOS-534 provou
	// atravessarem o proxy intactos (cerca de 5,6 MB em base64). Os da bateria têm poucas dezenas
	// de KB; um acima disto é um defeito do gerador, e recusa-se.
	MaxBytesDoVideo = 4 << 20
)

// CodificadorDeVideo faz o MP4 de um vídeo da bateria a partir dos seus fotogramas.
type CodificadorDeVideo interface {
	// Descricao identifica o codificador no relatório: a imagem e a versão do ffmpeg.
	Descricao() string
	// Codificar escreve os fotogramas (por `fotogramas`, em RGBA cru) e devolve o MP4.
	Codificar(ctx context.Context, v VideoDaBateria, fotogramas func(io.Writer) error) ([]byte, error)
}

// VideoPronto é um vídeo da bateria pronto para um motor: o MP4 (nil num motor que não o lê) e o
// seu digest.
type VideoPronto struct {
	Video VideoDaBateria
	MP4   []byte
	// Digest é o sha256 do MP4; vazio sem MP4.
	Digest string
}

// SemMP4 devolve os vídeos da bateria sem MP4, para um motor que não o lê (o falso).
func (b *BateriaVideo) SemMP4() []VideoPronto {
	out := make([]VideoPronto, len(b.Videos))
	for i, v := range b.Videos {
		out[i] = VideoPronto{Video: v}
	}
	return out
}

// mp4sEmCache guarda, por processo, os MP4 de uma bateria por um codificador: a chave é o digest
// da bateria e a descrição do codificador. Gerar os oito vídeos custa segundos; a mesma bateria,
// com o mesmo ffmpeg, dá os mesmos vídeos.
var (
	mp4sMu      sync.Mutex
	mp4sEmCache = map[string][]VideoPronto{}
)

// ComMP4 gera, pelo codificador, o MP4 de cada vídeo da bateria e confere-o. Fail-closed: um MP4
// que não seja o vídeo da bateria, ou maior do que [MaxBytesDoVideo], é um erro, e nenhum vídeo
// segue.
func (b *BateriaVideo) ComMP4(ctx context.Context, cod CodificadorDeVideo) ([]VideoPronto, error) {
	chave := b.Digest() + "\n" + cod.Descricao()
	mp4sMu.Lock()
	feitos, ok := mp4sEmCache[chave]
	mp4sMu.Unlock()
	if ok {
		return copiarVideos(feitos), nil
	}
	out := make([]VideoPronto, 0, len(b.Videos))
	for _, v := range b.Videos {
		mp4, err := cod.Codificar(ctx, v, func(w io.Writer) error { return b.escreverFotogramas(w, v) })
		if err != nil {
			return nil, fmt.Errorf("banco-ensaio: o MP4 do video %s: %w", v.ID, err)
		}
		if len(mp4) > MaxBytesDoVideo {
			return nil, fmt.Errorf("%w: o MP4 do video %s tem %d bytes, acima dos %d provados pelo proxy (AOS-534)", ErrMP4, v.ID, len(mp4), MaxBytesDoVideo)
		}
		forma, err := InspeccionarMP4(mp4)
		if err == nil {
			err = conferirMP4(forma, v)
		}
		if err != nil {
			return nil, fmt.Errorf("banco-ensaio: o MP4 do video %s: %w", v.ID, err)
		}
		out = append(out, VideoPronto{Video: v, MP4: mp4, Digest: digestDe(mp4)})
	}
	mp4sMu.Lock()
	mp4sEmCache[chave] = out
	mp4sMu.Unlock()
	return copiarVideos(out), nil
}

func copiarVideos(vs []VideoPronto) []VideoPronto {
	out := make([]VideoPronto, len(vs))
	for i, v := range vs {
		v.MP4 = append([]byte(nil), v.MP4...)
		out[i] = v
	}
	return out
}

// corredorDockerComEntrada corre um comando `docker` com o stdin dado e devolve o stdout, o
// stderr e o código de saída. Um erro só quando o processo nem chegou a dar código.
type corredorDockerComEntrada func(ctx context.Context, entrada io.Reader, args ...string) (stdout, stderr []byte, codigo int, err error)

func correrDockerComEntrada(ctx context.Context, entrada io.Reader, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- argumentos do banco: a imagem fixada, nomes gerados, dimensoes da bateria e uma pasta temporaria; nada vem de um modelo
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = entrada, &out, &errb
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

// CodificadorFFmpeg é o ffmpeg num contentor, com a imagem fixada.
type CodificadorFFmpeg struct {
	imagem, versao string
	correr         corredorDockerComEntrada
	limite         time.Duration
}

// ErrImagemDoFFmpeg — a imagem do ffmpeg não está no posto, ou não corre o ffmpeg.
var ErrImagemDoFFmpeg = errors.New("banco-ensaio: imagem do ffmpeg ausente ou invalida")

// NovoCodificadorFFmpeg confirma que a imagem fixada está no posto (sem a descarregar) e lê a
// versão do ffmpeg. `correr` nil ⇒ o Docker local.
func NovoCodificadorFFmpeg(ctx context.Context, correr corredorDockerComEntrada) (*CodificadorFFmpeg, error) {
	if correr == nil {
		correr = correrDockerComEntrada
	}
	if _, _, codigo, err := correr(ctx, nil, "image", "inspect", "--format", "{{.Id}}", ImagemDoFFmpeg); err != nil || codigo != 0 {
		return nil, fmt.Errorf("%w: a imagem %s nao esta no posto (ou o Docker nao responde). O banco nao a descarrega: o dono descarrega-a com  docker pull %s", ErrImagemDoFFmpeg, ImagemDoFFmpeg, ImagemDoFFmpeg)
	}
	out, _, codigo, err := correr(ctx, nil, "run", "--rm", "--pull", "never", "--network", "none", "--entrypoint", "ffmpeg", ImagemDoFFmpeg, "-hide_banner", "-version")
	if err != nil || codigo != 0 {
		return nil, fmt.Errorf("%w: a imagem %s nao corre o ffmpeg", ErrImagemDoFFmpeg, ImagemDoFFmpeg)
	}
	return &CodificadorFFmpeg{imagem: ImagemDoFFmpeg, versao: primeiraLinha(string(out)), correr: correr, limite: LimiteDeTempoDoFFmpeg}, nil
}

// Descricao implementa [CodificadorDeVideo].
func (c *CodificadorFFmpeg) Descricao() string {
	return fmt.Sprintf("%s; H.264 yuv420p +faststart; imagem %s", c.versao, c.imagem)
}

// argumentosDoFFmpeg devolve os argumentos do `docker run` de uma codificação.
func argumentosDoFFmpeg(contentor, pasta, imagem string, v VideoDaBateria) []string {
	return []string{
		"run", "--rm", "-i", "--pull", "never", "--name", contentor,
		"--network", "none",
		"--memory", strconv.FormatInt(LimiteDeMemoriaDoFFmpeg, 10), "--memory-swap", strconv.FormatInt(LimiteDeMemoriaDoFFmpeg, 10),
		"--pids-limit", strconv.Itoa(LimiteDeProcessosFFmpeg),
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--user", "65532:65532",
		"-v", pasta + ":/out",
		"--entrypoint", "ffmpeg", imagem,
		"-hide_banner", "-loglevel", "error", "-nostats",
		"-f", "rawvideo", "-pixel_format", "rgba", "-video_size", fmt.Sprintf("%dx%d", v.Largura, v.Altura), "-framerate", strconv.Itoa(v.FPS),
		"-i", "pipe:0",
		"-an", "-c:v", "libx264", "-preset", "veryfast", "-tune", "stillimage", "-pix_fmt", "yuv420p", "-threads", "1",
		"-fflags", "+bitexact", "-flags:v", "+bitexact", "-map_metadata", "-1",
		"-movflags", "+faststart", "-f", "mp4", "/out/" + nomeDoMP4,
	}
}

// nomeDoMP4 é o nome do ficheiro que o ffmpeg escreve na pasta montada.
const nomeDoMP4 = "video.mp4"

// Codificar implementa [CodificadorDeVideo].
func (c *CodificadorFFmpeg) Codificar(ctx context.Context, v VideoDaBateria, fotogramas func(io.Writer) error) ([]byte, error) {
	pasta, err := os.MkdirTemp("", "aos537-video-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(pasta)
	// A pasta nasce 0700; o contentor corre com o uid 65532, que num anfitrião Linux não escrevia
	// nela. Fica 0733: os outros (o uid do contentor) podem criar ficheiros nela, mas não listá-la;
	// e o MP4 lê-se só se for um ficheiro regular, sem seguir ligações ([lerMP4DaPasta]). Só durante
	// a codificação de um vídeo sintético; apagada no fim.
	if err := os.Chmod(pasta, 0o733); err != nil { // #nosec G302 -- pasta temporaria onde o uid do contentor escreve o MP4 sintetico; sem listagem para os outros; apagada no fim
		return nil, err
	}
	var sufixo [6]byte
	if _, err := rand.Read(sufixo[:]); err != nil {
		return nil, err
	}
	contentor := prefixoDoDocker + "video-" + hex.EncodeToString(sufixo[:])
	limitado, cancelar := context.WithTimeout(ctx, c.limite)
	defer cancelar()
	leitor, escritor := io.Pipe()
	go func() { escritor.CloseWithError(fotogramas(escritor)) }()
	_, errb, codigo, err := c.correr(limitado, leitor, argumentosDoFFmpeg(contentor, pasta, c.imagem, v)...)
	_ = leitor.Close()
	if limitado.Err() != nil {
		// O docker do lado de cá morreu com o contexto; o contentor pode continuar vivo.
		_, _, _, _ = c.correr(context.WithoutCancel(ctx), nil, "rm", "-f", contentor)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("o ffmpeg passou o limite de %s", c.limite)
	}
	if err != nil {
		return nil, err
	}
	if codigo != 0 {
		return nil, fmt.Errorf("o ffmpeg saiu com %d: %s", codigo, primeiraLinha(string(errb)))
	}
	return lerMP4DaPasta(filepath.Join(pasta, nomeDoMP4))
}

// lerMP4DaPasta lê o MP4 que o contentor deixou, sem confiar no que está no caminho: só um
// ficheiro regular (uma ligação simbólica, um dispositivo ou uma pasta são recusados), o ficheiro
// aberto tem de ser o mesmo que se viu antes de o abrir (uma troca entre os dois passos é
// recusada), e lê-se no máximo um byte acima de [MaxBytesDoVideo].
func lerMP4DaPasta(caminho string) ([]byte, error) {
	visto, err := os.Lstat(caminho)
	if err != nil {
		return nil, fmt.Errorf("o ffmpeg nao deixou o MP4: %w", err)
	}
	if !visto.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: o MP4 da pasta montada nao e um ficheiro regular (%s)", ErrMP4, visto.Mode().Type())
	}
	f, err := os.Open(caminho) // #nosec G304 -- ficheiro da pasta temporaria do proprio banco, conferido antes e depois de aberto
	if err != nil {
		return nil, err
	}
	defer f.Close()
	aberto, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(visto, aberto) || !aberto.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: o MP4 da pasta montada mudou entre ser visto e ser aberto", ErrMP4)
	}
	return io.ReadAll(io.LimitReader(f, MaxBytesDoVideo+1))
}
