package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"github.com/coreos/go-etcd/etcd"
	"golang.org/x/net/context"
)

var usage = func() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s MOUNTPOINT\n", os.Args[0])
	flag.PrintDefaults()
}

var etc = etcd.NewClient(nil) // FIXME: change nil to the []string of etcd servers if not localhost

func main() {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, os.Kill, syscall.SIGQUIT)

	etc.SetConsistency("WEAK_CONSISTENCY")

	flag.Usage = usage
	flag.Parse()

	if flag.NArg() != 1 {
		usage()
		os.Exit(2)
	}
	mountpoint := flag.Arg(0)

	c, err := fuse.Mount(
		mountpoint,
		fuse.FSName("etcdfs"),
		fuse.Subtype("etcdfs"),
		fuse.LocalVolume(),
		fuse.VolumeName("etcdfs"),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer fuse.Unmount(mountpoint)
	defer c.Close()

	go func() {
		for s := range signalChan {
			switch s {
			case syscall.SIGHUP:
				// reload config?  we're not actually subscribed to this
			default:
				fmt.Printf("Caught a %s signal, closing.\n", s)
				fuse.Unmount(mountpoint)
				os.Exit(1)
			}
		}
	}()

	err = fs.Serve(c, FS{})
	if err != nil {
		log.Fatal(err)
	}

	// check if the mount process has an error to report
	<-c.Ready
	if err := c.MountError; err != nil {
		log.Fatal(err)
	}
}

// FS implements file system
type FS struct{}

// Root of FS
func (FS) Root() (fs.Node, error) {
	return Dir{}, nil
}

// Dir implements both Node and Handle for directory handling
type Dir struct {
	Name string
	Dir  *Dir
}

// Attr of Dir
func (d Dir) Attr() fuse.Attr {
	fmt.Printf("DIR ATTR: [%s]\n", d.Name)
	res, err := etc.Get(filePath(&d, ""), true, true)
	if err != nil {
		return fuse.Attr{}
	}
	if res == nil || res.Node == nil {
		return fuse.Attr{}
	}
	mtime := time.Now().Add(time.Hour * 24 * 180 * -1)
	attr := fuse.Attr{
		Mode:  os.ModeDir | 0555, // FIXME: don't hardcode this.
		Size:  0,                 // FIXME: what should this be?
		Mtime: mtime,
		Atime: mtime,
		Ctime: mtime,
	}
	fmt.Printf("DIR ATTR: [%+v]\n", attr)
	return attr
}

// Lookup within Dir
func (d Dir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	fmt.Printf("DIR LOOKUP: [%s]\n", name)
	res, err := etc.Get(filePath(&d, name), true, true)
	if err != nil {
		return nil, fuse.ENOENT // FIXME: correct error response?
	}
	if res == nil || res.Node == nil {
		return nil, fuse.ENOENT // FIXME: correct error response?
	}
	if res.Node.Dir {
		return Dir{Name: name, Dir: &d}, nil
	}
	return File{Name: name, Dir: &d}, nil
}

// ReadDir of Dir
func (d Dir) ReadDir(ctx context.Context) ([]fuse.Dirent, error) {
	fmt.Printf("DIR READDIR: [%s]\n", d.Name)
	res, err := etc.Get(filePath(&d, ""), true, true)
	if err != nil {
		return nil, fuse.ENOENT // FIXME: correct error response?
	}
	if res == nil || res.Node == nil {
		return nil, fuse.ENOENT // FIXME: correct error response?
	}
	dirList := make([]fuse.Dirent, len(res.Node.Nodes)+2)
	dirList[0] = fuse.Dirent{
		Name: ".",
		Type: fuse.DT_Dir,
	}
	dirList[1] = fuse.Dirent{
		Name: "..",
		Type: fuse.DT_Dir,
	}
	if res.Node.Nodes != nil { // is this ever actually nil?
		for i, node := range res.Node.Nodes {
			_, fileName := filepath.Split(node.Key)
			fileType := fuse.DT_File
			if node.Dir {
				fileType = fuse.DT_Dir
			}
			dirList[i+2] = fuse.Dirent{
				Name: fileName,
				Type: fileType,
			}
		}
	}
	fmt.Printf("DIR LISTING: [%+v]\n", dirList)
	return dirList, nil
}

// File implements both Node and Handle for file handling
type File struct {
	Name string
	Dir  *Dir
}

// Attr of File
func (f File) Attr() fuse.Attr {
	fmt.Printf("FILE ATTR: [%s]\n", f.Name)
	res, err := etc.Get(filePath(f.Dir, f.Name), true, true)
	if err != nil {
		return fuse.Attr{}
	}
	if res == nil || res.Node == nil {
		return fuse.Attr{}
	}
	node := res.Node
	mtime := time.Now() // FIXME: obviously not legit
	attr := fuse.Attr{
		Mode:  0444, // FIXME: don't hardcode this.
		Size:  uint64(len(node.Value)),
		Mtime: mtime,
		Atime: mtime,
		Ctime: mtime,
	}
	fmt.Printf("FILE ATTR: [%+v]\n", attr)
	return attr
}

// ReadAll of File
func (f File) ReadAll(ctx context.Context) ([]byte, error) {
	fmt.Printf("READALL: [%s]\n", f.Name)
	res, err := etc.Get(filePath(f.Dir, f.Name), true, true)
	if err != nil {
		return nil, fuse.ENOENT
	}
	if res == nil || res.Node == nil {
		return nil, fuse.ENOENT
	}
	node := res.Node
	fmt.Printf("READALL: [%s] [%+v]\n", f.Name, node)
	return []byte(node.Value), nil
}

func etcdKeyNotFound(err error) bool { // FIXME: more things should be using this...
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Key not found")
}

func filePath(d *Dir, filename string) string {
	if d == nil {
		return filename
	}
	return filePath(d.Dir, filepath.Join(d.Name, filename))
}
