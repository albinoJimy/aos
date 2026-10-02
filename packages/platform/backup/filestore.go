package backup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// FileImmutableStore é um adaptador DURÁVEL da porta [ImmutableStore] sobre um DIRECTÓRIO LOCAL,
// só com a stdlib (AOS-453, fase F2). É o primeiro destino durável deste repositório: até aqui só
// existia a referência em memória.
//
// # Como cumpre o contrato da porta (README do módulo, §«Contrato da porta»)
//
//   - Put condicional e atómico: o blob é escrito num temporário no MESMO directório, `fsync`, e
//     publicado com `os.Link(tmp, final)`. O link falha se o nome final já existir — é o
//     equivalente local do `If-None-Match: *` do S3 — e essa falha é [ErrImmutable]. Não há janela
//     em que um leitor veja um objecto meio escrito: ou o nome não existe, ou aponta para um
//     ficheiro completo e sincronizado. A sonda `probeConditionalPut` do exportador passa.
//   - Get devolve [ErrNotFound] SÓ quando o ficheiro não existe; qualquer outro erro (permissões,
//     disco, um cabeçalho ilegível) propaga-se como erro — um destino que não responde nunca se lê
//     como «virgem».
//   - Delete respeita o object-lock: o instante de retenção viaja num cabeçalho do próprio objecto
//     (um só ficheiro, publicado atomicamente com o blob), e antes dele é [ErrObjectLocked].
//
// # O que NÃO protege, e fica dito
//
// É «só-de-escrita» perante o EXPORTADOR, não perante o sistema operativo. NÃO protege:
//   - da PERDA DO HOST ou do disco — é uma cópia local; a cópia fora do host é a fase F4 (S3 com
//     Object Lock), ainda não implementada;
//   - de ROOT, nem de quem tenha escrita no directório: um `rm` ou um `chmod` passam por cima do
//     write-once, que aqui é uma disciplina da porta e não um object-lock do armazenamento;
//   - de um restauro do volume para um ponto anterior (a retoma detecta parte disso — ver resume.go).
//
// O directório raiz tem de EXISTIR (o adaptador não o cria): um caminho mal escrito criaria em
// silêncio um destino novo e vazio, e o exportador anunciaria «cadeia NOVA» por cima de um backup
// que existe noutro sítio.
type FileImmutableStore struct {
	root   string
	region string
}

// fileObjectMagic abre o cabeçalho de cada objecto. Versionado, para um formato futuro se
// distinguir deste sem ambiguidade.
const fileObjectMagic = "AOS-BACKUP-OBJ/1\n"

// NewFileImmutableStore constrói o adaptador sobre root (caminho ABSOLUTO de um directório que já
// existe) para a região de soberania dada. Fail-closed: caminho relativo, inexistente ou que não
// seja um directório ⇒ [ErrConfig].
func NewFileImmutableStore(root, region string) (*FileImmutableStore, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: o destino em disco tem de ser um caminho ABSOLUTO (recebido %q)", ErrConfig, root)
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("%w: o directorio do destino %q nao e acessivel (tem de existir — o adaptador nao o cria): %v", ErrConfig, root, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%w: o destino %q nao e um directorio", ErrConfig, root)
	}
	return &FileImmutableStore{root: filepath.Clean(root), region: normalizeRegion(region)}, nil
}

// Region implementa [ImmutableStore].
func (s *FileImmutableStore) Region() string { return s.region }

// Root devolve o directório raiz (para banners e diagnóstico; não é segredo).
func (s *FileImmutableStore) Root() string { return s.root }

// String descreve o destino para os banners do nó.
func (s *FileImmutableStore) String() string {
	return fmt.Sprintf("file://%s (regiao %q)", filepath.ToSlash(s.root), s.region)
}

// path valida ref e devolve o caminho do objecto. Uma ref é uma sequência de segmentos separados
// por '/', cada um não-vazio, sem começar por '.' (reservado aos temporários, e exclui `.`/`..`) e
// só com [A-Za-z0-9._-]. Nada fora da raiz é alcançável.
func (s *FileImmutableStore) path(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("%w: ref vazia", ErrConfig)
	}
	for _, seg := range strings.Split(ref, "/") {
		if seg == "" || seg[0] == '.' {
			return "", fmt.Errorf("%w: ref %q invalida (segmento vazio ou iniciado por '.')", ErrConfig, ref)
		}
		for _, r := range seg {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
			if !ok {
				return "", fmt.Errorf("%w: ref %q com caracter %q fora do alfabeto do destino", ErrConfig, ref, r)
			}
		}
	}
	return filepath.Join(s.root, filepath.FromSlash(ref)), nil
}

// objectPerm é a permissão do objecto publicado: só leitura. Em Windows um ficheiro só-leitura não
// se apaga com os.Remove, e o temporário (o outro nome do mesmo ficheiro) tem de sair — aí fica
// 0600; o alvo de produção é Linux.
func objectPerm() os.FileMode {
	if runtime.GOOS == "windows" {
		return 0o600
	}
	return 0o440
}

// Put implementa [ImmutableStore]: write-once condicional e atómico (tmp + fsync + link).
func (s *FileImmutableStore) Put(ref string, blob []byte, retainUntil time.Time) error {
	final, err := s.path(ref)
	if err != nil {
		return err
	}
	dir := filepath.Dir(final)
	if err := s.ensureDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return fmt.Errorf("backup: destino em disco: criar temporario em %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // o nome final (se publicado) é outro link: sobrevive
	var hdr bytes.Buffer
	hdr.WriteString(fileObjectMagic)
	hdr.WriteString("retain-until=")
	hdr.WriteString(retainUntil.UTC().Format(time.RFC3339Nano))
	hdr.WriteByte('\n')
	_, werr := tmp.Write(append(hdr.Bytes(), blob...))
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("backup: destino em disco: escrever %q: %w", ref, werr)
	}
	if err := os.Chmod(tmpName, objectPerm()); err != nil {
		return fmt.Errorf("backup: destino em disco: permissoes de %q: %w", ref, err)
	}
	if err := os.Link(tmpName, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrImmutable
		}
		return fmt.Errorf("backup: destino em disco: publicar %q: %w", ref, err)
	}
	// O link só é durável quando a entrada do directório o for.
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("backup: destino em disco: fsync do directorio de %q: %w", ref, err)
	}
	return nil
}

// ensureDir cria (0700) o directório do objecto dentro da raiz, e sincroniza o pai quando o criou.
func (s *FileImmutableStore) ensureDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("backup: destino em disco: criar %q: %w", dir, err)
	}
	if err := syncDir(filepath.Dir(dir)); err != nil {
		return fmt.Errorf("backup: destino em disco: fsync de %q: %w", filepath.Dir(dir), err)
	}
	return nil
}

// syncDir faz fsync de um directório. Em Windows não se abre um directório para sync; o alvo de
// produção é Linux, onde isto é o que torna um link/criação duráveis.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// read lê o objecto e separa o cabeçalho. Inexistente ⇒ [ErrNotFound]; ilegível ⇒ erro (nunca
// ErrNotFound).
func (s *FileImmutableStore) read(ref string) (blob []byte, retainUntil time.Time, err error) {
	p, err := s.path(ref)
	if err != nil {
		return nil, time.Time{}, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, time.Time{}, ErrNotFound
		}
		return nil, time.Time{}, fmt.Errorf("backup: destino em disco: ler %q: %w", ref, err)
	}
	if !bytes.HasPrefix(raw, []byte(fileObjectMagic)) {
		return nil, time.Time{}, fmt.Errorf("backup: destino em disco: o objecto %q nao tem o cabecalho %q (nao foi escrito por este adaptador, ou foi adulterado)", ref, strings.TrimSpace(fileObjectMagic))
	}
	rest := raw[len(fileObjectMagic):]
	nl := bytes.IndexByte(rest, '\n')
	if nl < 0 || !bytes.HasPrefix(rest[:nl], []byte("retain-until=")) {
		return nil, time.Time{}, fmt.Errorf("backup: destino em disco: cabecalho de retencao ilegivel em %q", ref)
	}
	ru, perr := time.Parse(time.RFC3339Nano, string(rest[len("retain-until="):nl]))
	if perr != nil {
		return nil, time.Time{}, fmt.Errorf("backup: destino em disco: instante de retencao ilegivel em %q: %w", ref, perr)
	}
	return rest[nl+1:], ru, nil
}

// Get implementa [ImmutableStore].
func (s *FileImmutableStore) Get(ref string) ([]byte, error) {
	blob, _, err := s.read(ref)
	return blob, err
}

// Delete implementa [ImmutableStore]: recusado dentro do object-lock (now < retainUntil).
func (s *FileImmutableStore) Delete(ref string, now time.Time) error {
	_, ru, err := s.read(ref)
	if err != nil {
		return err
	}
	if now.Before(ru) {
		return ErrObjectLocked
	}
	p, _ := s.path(ref) // já validada por read
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("backup: destino em disco: remover %q: %w", ref, err)
	}
	return nil
}

var _ ImmutableStore = (*FileImmutableStore)(nil)
