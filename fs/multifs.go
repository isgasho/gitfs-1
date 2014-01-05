package fs

import (
	"log"
	"time"
	"syscall"
	"strings"
	"os"
	
	git "github.com/libgit2/git2go"

	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
	"github.com/hanwen/go-fuse/fuse"
)

type multiGitFS struct {
	fsConn *nodefs.FileSystemConnector
	root    nodefs.Node
}

func NewMultiGitFSRoot() nodefs.Node {
	fs := &multiGitFS{}
	fs.root = &multiGitRoot{nodefs.NewDefaultNode(), fs}
	return fs.root
}

type multiGitRoot struct {
	nodefs.Node
	fs *multiGitFS
}

func (r *multiGitRoot) OnMount(fsConn *nodefs.FileSystemConnector) {
	r.fs.fsConn = fsConn
	r.Inode().NewChild("config", true, r.fs.newConfigNode(r))
}


type configNode struct {
	fs *multiGitFS
	
	nodefs.Node

	// non-config node corresponding to this one.
	corresponding nodefs.Node
}

func (fs *multiGitFS) newConfigNode(corresponding nodefs.Node) *configNode {
	return &configNode{
		fs: fs,
		Node: nodefs.NewDefaultNode(),
		corresponding: corresponding,
	}
}

type gitConfigNode struct {
	nodefs.Node

	content string
}

func newGitConfigNode(content string) *gitConfigNode {
	return &gitConfigNode{
		Node: nodefs.NewDefaultNode(),
		content: content,
	}
}

func (n *gitConfigNode) GetAttr(out *fuse.Attr, file nodefs.File, context *fuse.Context) (code fuse.Status) {
	out.Mode = syscall.S_IFLNK
	return fuse.OK
}

func (n *gitConfigNode) Readlink(c *fuse.Context) ([]byte, fuse.Status) {
	return []byte(n.content), fuse.OK
}

func (n *configNode) Mkdir(name string, mode uint32, context *fuse.Context) (nodefs.Node, fuse.Status) {
	corr := n.corresponding.Inode().NewChild(name, true, nodefs.NewDefaultNode())
	c := n.fs.newConfigNode(corr.Node())
	n.Inode().NewChild(name, true, c)
	return c, fuse.OK
}

func (n *configNode) Unlink(name string, context *fuse.Context) (code fuse.Status) {
	linkInode := n.Inode().GetChild(name)
	if linkInode == nil {
		return fuse.ENOENT
	}

	_, ok := linkInode.Node().(*gitConfigNode)
	if !ok {
		log.Printf("gitfs: removing %q, child is not a gitConfigNode", name)
		return fuse.EINVAL
	}

	root := n.corresponding.Inode().GetChild(name)
	if root == nil {
		return fuse.EINVAL
	}

	code = n.fs.fsConn.Unmount(root)
	if code.Ok() {
		n.Inode().RmChild(name)
	}
	return code
}

func (n *configNode) Symlink(name string, content string, context *fuse.Context) (newNode nodefs.Node, code fuse.Status) {
	dir := content
	components := strings.Split(content, ":")
	if len(components) > 2 || len(components) == 0{
		return nil, fuse.Status(syscall.EINVAL)
	}
	if len(components) == 2 {
		dir = components[0]
	}
	
	if fi, err := os.Lstat(dir); err != nil {
		return nil, fuse.ToStatus(err)
	} else if !fi.IsDir() {
		return nil, fuse.Status(syscall.ENOTDIR)
	}

	var opts *nodefs.Options
	var root nodefs.Node
	if len(components) == 1 {
		root = pathfs.NewPathNodeFs(pathfs.NewLoopbackFileSystem(content), nil).Root()
	} else {
		repo, err := git.OpenRepository(components[0])
		if err != nil {
			log.Printf("OpenRepository(%q): %v", components[0], err)
			return nil, fuse.ENOENT
		}
		
		root, err = NewTreeFSRoot(repo, components[1])
		if err != nil {
			log.Printf("NewTreeFSRoot(%q): %v", components[1], err)
			return nil, fuse.ENOENT
		}

		opts = &nodefs.Options{
			EntryTimeout: time.Hour,
			NegativeTimeout: time.Hour,
			AttrTimeout: time.Hour,
			PortableInodes: true,
		}
	}
	
	if code := n.fs.fsConn.Mount(n.corresponding.Inode(), name, root, opts); !code.Ok() {
		return nil, code
	}

	linkNode := newGitConfigNode(content)
	n.Inode().NewChild(name, false, linkNode)
	return linkNode, fuse.OK
}
