package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/buger/jsonparser"
	"github.com/jfrog/gofrog/io"
	"github.com/jfrog/jfrog-cli-go/artifactory/commands/generic"
	"github.com/jfrog/jfrog-cli-go/artifactory/spec"
	"github.com/jfrog/jfrog-cli-go/artifactory/utils"
	"github.com/jfrog/jfrog-cli-go/inttestutils"
	"github.com/jfrog/jfrog-cli-go/utils/cliutils"
	"github.com/jfrog/jfrog-cli-go/utils/config"
	logUtils "github.com/jfrog/jfrog-cli-go/utils/log"
	"github.com/jfrog/jfrog-cli-go/utils/tests"
	cliproxy "github.com/jfrog/jfrog-cli-go/utils/tests/proxy/server"
	"github.com/jfrog/jfrog-cli-go/utils/tests/proxy/server/certificate"
	"github.com/jfrog/jfrog-client-go/artifactory/auth"
	"github.com/jfrog/jfrog-client-go/artifactory/services"
	rtutils "github.com/jfrog/jfrog-client-go/artifactory/services/utils"
	"github.com/jfrog/jfrog-client-go/artifactory/services/utils/tests/xray"
	"github.com/jfrog/jfrog-client-go/httpclient"
	clientutils "github.com/jfrog/jfrog-client-go/utils"
	"github.com/jfrog/jfrog-client-go/utils/errorutils"
	"github.com/jfrog/jfrog-client-go/utils/io/fileutils"
	"github.com/jfrog/jfrog-client-go/utils/io/httputils"
	"github.com/jfrog/jfrog-client-go/utils/log"
	"github.com/mholt/archiver"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// JFrog CLI for Artifactory commands
var artifactoryCli *tests.JfrogCli

// JFrog CLI for config command only (doesn't pass the --ssh-passphrase flag)
var configArtifactoryCli *tests.JfrogCli

var artifactoryDetails *config.ArtifactoryDetails
var artAuth auth.ArtifactoryDetails
var artHttpDetails httputils.HttpClientDetails

func InitArtifactoryTests() {
	if !*tests.TestArtifactory {
		return
	}
	os.Setenv(cliutils.ReportUsage, "false")
	os.Setenv(cliutils.OfferConfig, "false")
	// Disable progress bar:
	os.Setenv("CI", "true")
	cred := authenticate()
	artifactoryCli = tests.NewJfrogCli(execMain, "jfrog rt", cred)
	configArtifactoryCli = createConfigJfrogCLI(cred)
	createReposIfNeeded()
	cleanArtifactoryTest()
}

func authenticate() string {
	artifactoryDetails = &config.ArtifactoryDetails{Url: clientutils.AddTrailingSlashIfNeeded(*tests.RtUrl), SshKeyPath: *tests.RtSshKeyPath, SshPassphrase: *tests.RtSshPassphrase, AccessToken: *tests.RtAccessToken}
	cred := "--url=" + *tests.RtUrl
	if !fileutils.IsSshUrl(artifactoryDetails.Url) {
		if *tests.RtApiKey != "" {
			artifactoryDetails.ApiKey = *tests.RtApiKey
		} else if *tests.RtAccessToken != "" {
			artifactoryDetails.AccessToken = *tests.RtAccessToken
		} else {
			artifactoryDetails.User = *tests.RtUser
			artifactoryDetails.Password = *tests.RtPassword
		}
	}
	cred += getArtifactoryTestCredentials()
	var err error
	if artAuth, err = artifactoryDetails.CreateArtAuthConfig(); err != nil {
		cliutils.ExitOnErr(errors.New("Failed while attempting to authenticate with Artifactory: " + err.Error()))
	}
	artifactoryDetails.SshAuthHeaders = artAuth.GetSshAuthHeaders()
	artifactoryDetails.Url = artAuth.GetUrl()
	artifactoryDetails.SshUrl = artAuth.GetSshUrl()
	artHttpDetails = artAuth.CreateHttpClientDetails()
	return cred
}

// A Jfrog CLI to be used to execute a config task.
// Removed the ssh-passphrase flag that cannot be passed to with a config command
func createConfigJfrogCLI(cred string) *tests.JfrogCli {
	if strings.Contains(cred, " --ssh-passphrase=") {
		cred = strings.Replace(cred, " --ssh-passphrase="+*tests.RtSshPassphrase, "", -1)
	}
	return tests.NewJfrogCli(execMain, "jfrog rt", cred)
}

func getArtifactoryTestCredentials() string {
	if fileutils.IsSshUrl(artifactoryDetails.Url) {
		return getSshCredentials()
	}
	if *tests.RtApiKey != "" {
		return " --apikey=" + *tests.RtApiKey
	}
	if *tests.RtAccessToken != "" {
		return " --access-token=" + *tests.RtAccessToken
	}
	return " --user=" + *tests.RtUser + " --password=" + *tests.RtPassword
}

func getSshCredentials() string {
	cred := ""
	if *tests.RtSshKeyPath != "" {
		cred += " --ssh-key-path=" + *tests.RtSshKeyPath
	}
	if *tests.RtSshPassphrase != "" {
		cred += " --ssh-passphrase=" + *tests.RtSshPassphrase
	}
	return cred
}

func TestArtifactorySimpleUploadSpec(t *testing.T) {
	initArtifactoryTest(t)
	specFile, err := tests.CreateSpec(tests.SimpleUploadSpec)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFile)
	searchFilePath, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetSimpleUploadExpectedRepo1(), searchFilePath, t)
	cleanArtifactoryTest()
}

// This test is similar to TestArtifactorySimpleUploadSpec but using "--server-id" flag
func TestArtifactorySimpleUploadSpecUsingConfig(t *testing.T) {
	initArtifactoryTest(t)
	passphrase := createServerConfigAndReturnPassphrase()
	artifactoryCommandExecutor := tests.NewJfrogCli(execMain, "jfrog rt", "")
	specFile, err := tests.CreateSpec(tests.SimpleUploadSpec)
	artifactoryCommandExecutor.Exec("upload", "--spec="+specFile, "--server-id="+tests.RtServerId, passphrase)

	searchFilePath, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetSimpleUploadExpectedRepo1(), searchFilePath, t)
	deleteServerConfig()
	cleanArtifactoryTest()
}

func TestArtifactoryUploadPathWithSpecialCharsAsNoRegex(t *testing.T) {
	initArtifactoryTest(t)
	filePath := getSpecialCharFilePath()

	artifactoryCli.Exec("upload", filePath, tests.Repo1)
	searchFilePath, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetSimpleUploadSpecialCharNoRegexExpectedRepo1(), searchFilePath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryDownloadFromVirtual(t *testing.T) {
	initArtifactoryTest(t)

	artifactoryCli.Exec("upload", "testsdata/a/*", tests.Repo1, "--flat=false")
	artifactoryCli.Exec("dl", tests.VirtualRepo+"/testsdata/(*)", tests.Out+"/"+"{1}", "--flat=true")

	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	tests.IsExistLocally(tests.GetVirtualDownloadExpected(), paths, t)

	cleanArtifactoryTest()
}

func TestArtifactoryDownloadPathWithSpecialChars(t *testing.T) {
	initArtifactoryTest(t)
	artifactoryCli.Exec("upload", getSpecialCharFilePath(), tests.Repo1, "--flat=false")
	artifactoryCli.Exec("upload", "testsdata/c#/a#1.in", tests.Repo1, "--flat=false")

	artifactoryCli.Exec("dl", tests.Repo1+"/testsdata/a$+~&^a#/a*", tests.Out+fileutils.GetFileSeparator(), "--flat=true")
	artifactoryCli.Exec("dl", tests.Repo1+"/testsdata/c#/a#1.in", tests.Out+fileutils.GetFileSeparator(), "--flat=true")

	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	tests.IsExistLocally([]string{tests.Out + fileutils.GetFileSeparator() + "a1.in", tests.Out + fileutils.GetFileSeparator() + "a#1.in"}, paths, t)

	cleanArtifactoryTest()
}

func TestArtifactoryConcurrentDownload(t *testing.T) {
	testArtifactoryDownload(cliutils.DownloadMinSplitKb*1000, t)
}

func TestArtifactoryBulkDownload(t *testing.T) {
	testArtifactoryDownload(cliutils.DownloadMinSplitKb*1000-1, t)
}

func testArtifactoryDownload(fileSize int, t *testing.T) {
	initArtifactoryTest(t)
	err := fileutils.CreateDirIfNotExist(tests.Out)
	if err != nil {
		t.Error(err)
	}
	randFile, err := io.CreateRandFile(filepath.Join(tests.Out, "randFile"), fileSize)
	if err != nil {
		t.Error(err)
	}
	localFileDetails, err := fileutils.GetFileDetails(randFile.Name())
	if err != nil {
		t.Error(err)
	}

	artifactoryCli.Exec("u", randFile.Name(), tests.Repo1+"/testsdata/", "--flat=true")
	randFile.File.Close()
	os.RemoveAll(tests.Out)
	artifactoryCli.Exec("dl", tests.Repo1+"/testsdata/", tests.Out+fileutils.GetFileSeparator(), "--flat=true")

	paths, err := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	if err != nil {
		t.Error(err)
	}
	tests.IsExistLocally([]string{tests.Out + fileutils.GetFileSeparator() + "randFile"}, paths, t)
	tests.ValidateChecksums(tests.Out+fileutils.GetFileSeparator()+"randFile", localFileDetails.Checksum, t)
	cleanArtifactoryTest()
}

func TestArtifactoryCopySingleFileNonFlat(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()

	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/")
	artifactoryCli.Exec("cp", tests.Repo1+"/path/a1.in", tests.Repo2)
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetSingleFileCopy(), searchPath, t)
	cleanArtifactoryTest()
}

func TestAqlFindingItemOnRoot(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()

	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/inner/")
	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/someFile", "--flat=true")
	artifactoryCli.Exec("cp", tests.Repo1+"/*", tests.Repo2)
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetAnyItemCopy(), searchPath, t)
	artifactoryCli.Exec("del", tests.Repo2+"/*", "--quiet=true")
	artifactoryCli.Exec("del", tests.Repo1+"/*", "--quiet=true")
	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/")
	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/someFile", "--flat=true")
	artifactoryCli.Exec("cp", tests.Repo1+"/*/", tests.Repo2)
	isExistInArtifactory(tests.GetSingleFileCopy(), searchPath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryDirectoryCopy(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()

	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/")
	artifactoryCli.Exec("cp", tests.Repo1+"/path/", tests.Repo2)
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetSingleFileCopy(), searchPath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryDirectoryCopyUsingWildcard(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()

	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/")
	artifactoryCli.Exec("cp", tests.Repo1+"/*/", tests.Repo2)
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetSingleFileCopy(), searchPath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryDirectoryCopyUsingWildcardFlat(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()

	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/inner/")
	artifactoryCli.Exec("cp", tests.Repo1+"/path/inner", tests.Repo2, "--flat=true")
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetSingleDirectoryCopyFlat(), searchPath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryCopyPathsTwice(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()
	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/inner/")

	log.Info("Copy Folder to root twice")
	artifactoryCli.Exec("cp", tests.Repo1+"/path", tests.Repo2)
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetSingleFileCopyFullPath(), searchPath, t)
	artifactoryCli.Exec("cp", tests.Repo1+"/path", tests.Repo2)
	isExistInArtifactory(tests.GetSingleFileCopyFullPath(), searchPath, t)
	artifactoryCli.Exec("del", tests.Repo2, "--quiet=true")

	log.Info("Copy to from repo1/path to repo2/path twice")
	artifactoryCli.Exec("cp", tests.Repo1+"/path", tests.Repo2+"/path")
	isExistInArtifactory(tests.GetSingleFileCopyFullPath(), searchPath, t)
	artifactoryCli.Exec("cp", tests.Repo1+"/path", tests.Repo2+"/path")
	isExistInArtifactory(tests.GetFolderCopyTwice(), searchPath, t)
	artifactoryCli.Exec("del", tests.Repo2, "--quiet=true")

	log.Info("Copy to from repo1/path/ to repo2/path/ twice")
	artifactoryCli.Exec("cp", tests.Repo1+"/path/", tests.Repo2+"/path/")
	isExistInArtifactory(tests.GetSingleInnerFileCopyFullPath(), searchPath, t)
	artifactoryCli.Exec("cp", tests.Repo1+"/path/", tests.Repo2+"/path/")
	isExistInArtifactory(tests.GetSingleInnerFileCopyFullPath(), searchPath, t)
	artifactoryCli.Exec("del", tests.Repo2, "--quiet=true")

	log.Info("Copy to from repo1/path/ to repo2/path/ twice")
	artifactoryCli.Exec("cp", tests.Repo1+"/path", tests.Repo2+"/path/")
	isExistInArtifactory(tests.GetFolderCopyIntoFolder(), searchPath, t)
	artifactoryCli.Exec("cp", tests.Repo1+"/path", tests.Repo2+"/path/")
	isExistInArtifactory(tests.GetFolderCopyIntoFolder(), searchPath, t)
	artifactoryCli.Exec("del", tests.Repo2, "--quiet=true")

	cleanArtifactoryTest()
}

func TestArtifactoryDirectoryCopyPatternEndsWithSlash(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()

	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/inner/")
	artifactoryCli.Exec("cp", tests.Repo1+"/path/", tests.Repo2, "--flat=true")
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetAnyItemCopyUsingSpec(), searchPath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryCopyAnyItemUsingWildcardFlat(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()

	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/inner/")
	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/someFile", "--flat=true")
	artifactoryCli.Exec("cp", tests.Repo1+"/*", tests.Repo2)
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetAnyItemCopy(), searchPath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryCopyAnyItemRecursive(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()

	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/a/b/")
	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/aFile", "--flat=true")
	artifactoryCli.Exec("cp", tests.Repo1+"/a*", tests.Repo2, "--recursive=true")
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetAnyItemCopyRecursive(), searchPath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryCopyAndRenameFolder(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()

	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/inner/")
	artifactoryCli.Exec("cp", tests.Repo1+"/*", tests.Repo2+"/newPath")
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetCopyFolderRename(), searchPath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryCopyAnyItemUsingSpec(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = getSpecialCharFilePath()

	specFile, err := tests.CreateSpec(tests.CopyItemsSpec)
	if err != nil {
		t.Error(err)
	}
	searchPath, err := tests.CreateSpec(tests.SearchRepo2)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/path/inner/")
	artifactoryCli.Exec("upload", filePath, tests.Repo1+"/someFile", "--flat=true")
	artifactoryCli.Exec("cp", "--spec="+specFile)
	isExistInArtifactory(tests.GetAnyItemCopyUsingSpec(), searchPath, t)
	cleanArtifactoryTest()
}

func getSpecialCharFilePath() string {
	return "testsdata/a$+~&^a#/a*"
}

func TestArtifactoryCopyNoSpec(t *testing.T) {
	testCopyMoveNoSpec("cp", tests.GetBuildBeforeCopyExpected(), tests.GetBuildCopyExpected(), t)
}

func TestArtifactoryCopyExcludeByCli(t *testing.T) {
	initArtifactoryTest(t)

	// Upload files
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA)
	artifactoryCli.Exec("upload", "--spec="+specFileB)

	// Copy by pattern
	artifactoryCli.Exec("cp", tests.Repo1+"/data/ "+tests.Repo2+"/", "--exclude-patterns=*b*;*c*")

	// Validate files are moved by build number
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetBuildCopyExclude(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryCopyExcludeBySpec(t *testing.T) {
	initArtifactoryTest(t)

	// Upload files
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA)
	artifactoryCli.Exec("upload", "--spec="+specFileB)

	// Copy by spec
	specFile, err := tests.CreateSpec(tests.MoveCopySpecExclude)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("cp", "--spec="+specFile)
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}

	// Validate files are moved by build number
	isExistInArtifactory(tests.GetBuildCopyExclude(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryUploadDebian(t *testing.T) {
	initArtifactoryTest(t)
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--deb=bionic/main/i386")
	isExistInArtifactoryByProps(tests.GetUploadDebianExpected(), tests.Repo1+"/*", "deb.distribution=bionic;deb.component=main;deb.architecture=i386", t)
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--deb=cosmic/main\\/18.10/amd64")
	isExistInArtifactoryByProps(tests.GetUploadDebianExpected(), tests.Repo1+"/*", "deb.distribution=cosmic;deb.component=main/18.10;deb.architecture=amd64", t)
	cleanArtifactoryTest()
}

func TestArtifactoryUploadAndExplode(t *testing.T) {
	initArtifactoryTest(t)
	artifactoryCli.Exec("upload", filepath.Join("testsdata", "archives", "a.zip"), tests.Repo1, "--explode=true")
	searchFilePath, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetExplodeUploadExpectedRepo1(), searchFilePath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryDownloadAndExplode(t *testing.T) {
	initArtifactoryTest(t)
	err := fileutils.CreateDirIfNotExist(tests.Out)
	if err != nil {
		t.Error(err)
	}
	randFile, err := io.CreateRandFile(filepath.Join(tests.Out, "randFile"), 100000)
	if err != nil {
		t.Error(err)
	}

	err = archiver.TarGz.Make(filepath.Join(tests.Out, "concurrent.tar.gz"), []string{randFile.Name()})
	if err != nil {
		t.Error(err)
	}
	err = archiver.Tar.Make(filepath.Join(tests.Out, "bulk.tar"), []string{randFile.Name()})
	if err != nil {
		t.Error(err)
	}
	err = archiver.Zip.Make(filepath.Join(tests.Out, "zipFile.zip"), []string{randFile.Name()})
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", tests.Out+"/*", tests.Repo1, "--flat=true")
	randFile.File.Close()
	os.RemoveAll(tests.Out)
	artifactoryCli.Exec("download", path.Join(tests.Repo1, "randFile"), tests.Out+"/", "--explode=true")
	artifactoryCli.Exec("download", path.Join(tests.Repo1, "concurrent.tar.gz"), tests.Out+"/", "--explode=false", "--min-split=50")
	artifactoryCli.Exec("download", path.Join(tests.Repo1, "bulk.tar"), tests.Out+"/", "--explode=true")
	artifactoryCli.Exec("download", path.Join(tests.Repo1, "zipFile.zip"), tests.Out+"/", "--explode=true", "--min-split=50")
	artifactoryCli.Exec("download", path.Join(tests.Repo1, "zipFile.zip"), tests.Out+"/", "--explode=true")

	paths, err := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	if err != nil {
		t.Error(err)
	}
	tests.IsExistLocally(tests.GetExtractedDownload(), paths, t)

	cleanArtifactoryTest()
}

// Test self-signed certificates with Artifactory. For the test, we set up a reverse proxy server.
func TestArtifactorySelfSignedCert(t *testing.T) {
	initArtifactoryTest(t)
	tempDirPath, err := ioutil.TempDir("", "jfrog.cli.test.")
	err = errorutils.CheckError(err)
	if err != nil {
		t.Error(err)
	}
	defer os.RemoveAll(tempDirPath)
	os.Setenv(cliutils.JfrogHomeDirEnv, tempDirPath)
	os.Setenv(tests.HttpsProxyEnvVar, "1024")
	go cliproxy.StartLocalReverseHttpProxy(artifactoryDetails.Url)

	// The two certificate files are created by the reverse proxy on startup in the current directory.
	defer os.Remove(certificate.KEY_FILE)
	defer os.Remove(certificate.CERT_FILE)
	// Let's wait for the reverse proxy to start up.
	err = checkIfServerIsUp(cliproxy.GetProxyHttpsPort(), "https")
	if err != nil {
		t.Error(err)
	}

	fileSpec := spec.NewBuilder().Pattern(tests.Repo1 + "/*.zip").Recursive(true).BuildSpec()
	if err != nil {
		t.Error(err)
	}
	parsedUrl, err := url.Parse(artifactoryDetails.Url)
	artifactoryDetails.Url = "https://127.0.0.1:" + cliproxy.GetProxyHttpsPort() + parsedUrl.RequestURI()

	// The server is using self-signed certificates
	// Without loading the certificates (or skipping verification) we expect all actions to fail due to error: "x509: certificate signed by unknown authority"
	searchCmd := generic.NewSearchCommand()
	searchCmd.SetRtDetails(artifactoryDetails).SetSpec(fileSpec)
	err = searchCmd.Search()
	if _, ok := err.(*url.Error); !ok {
		t.Error("Expected a connection failure, since reverse proxy didn't load self-signed-certs. Connection however is successful", err)
	}

	// Set insecureTls to true and run again. We expect the command to succeed.
	artifactoryDetails.InsecureTls = true
	searchCmd = generic.NewSearchCommand()
	searchCmd.SetRtDetails(artifactoryDetails).SetSpec(fileSpec)
	err = searchCmd.Search()
	if err != nil {
		t.Error(err)
	}

	// Set insecureTls back to false.
	// Copy the server certificates to the CLI security dir and run again. We expect the command to succeed.
	artifactoryDetails.InsecureTls = false
	securityDirPath, err := utils.GetJfrogSecurityDir()
	if err != nil {
		t.Error(err)
	}
	err = fileutils.CopyFile(securityDirPath, certificate.KEY_FILE)
	if err != nil {
		t.Error(err)
	}
	err = fileutils.CopyFile(securityDirPath, certificate.CERT_FILE)
	if err != nil {
		t.Error(err)
	}
	searchCmd = generic.NewSearchCommand()
	searchCmd.SetRtDetails(artifactoryDetails).SetSpec(fileSpec)
	err = searchCmd.Search()
	if err != nil {
		t.Error(err)
	}

	artifactoryDetails.Url = artAuth.GetUrl()
	cleanArtifactoryTest()
}

func getExternalIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // interface down
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue // loopback interface
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return "", err
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue // not an ipv4 address
			}
			return ip.String(), nil
		}
	}
	return "", errors.New("are you connected to the network?")
}

// Due the fact that go read the HTTP_PROXY and the HTTPS_PROXY
// argument only once we can't set the env var for specific test.
// We need to start a new process with the env var set to the value we want.
// We decide which var to set by the rtUrl scheme.
func TestArtifactoryProxy(t *testing.T) {
	initArtifactoryTest(t)
	rtUrl, err := url.Parse(artifactoryDetails.Url)
	if err != nil {
		t.Error(err)
	}
	var proxyTestArgs []string
	var httpProxyEnv string
	testArgs := []string{"-test.artifactoryProxy=true", "-rt.url=" + *tests.RtUrl, "-rt.user=" + *tests.RtUser, "-rt.password=" + *tests.RtPassword, "-rt.apikey=" + *tests.RtApiKey, "-rt.sshKeyPath=" + *tests.RtSshKeyPath, "-rt.sshPassphrase=" + *tests.RtSshPassphrase}
	if rtUrl.Scheme == "https" {
		os.Setenv(tests.HttpsProxyEnvVar, "1025")
		proxyTestArgs = append([]string{"test", "-run=TestArtifactoryHttpsProxyEnvironmentVariableDelegator"}, testArgs...)
		httpProxyEnv = "HTTPS_PROXY=localhost:" + cliproxy.GetProxyHttpsPort()
	} else {
		proxyTestArgs = append([]string{"test", "-run=TestArtifactoryHttpProxyEnvironmentVariableDelegator"}, testArgs...)
		httpProxyEnv = "HTTP_PROXY=localhost:" + cliproxy.GetProxyHttpPort()
	}
	runProxyTest(t, proxyTestArgs, httpProxyEnv)
	cleanArtifactoryTest()
}

func runProxyTest(t *testing.T, proxyTestArgs []string, httpProxyEnv string) {
	cmd := exec.Command("go", proxyTestArgs...)
	cmd.Env = append(os.Environ(), httpProxyEnv)

	tempDirPath, err := tests.GetTestsLogsDir()
	if err != nil {
		t.Error(err)
	}
	f, err := os.Create(filepath.Join(tempDirPath, "artifactory_proxy_tests.log"))
	if err != nil {
		t.Error(err)
	}

	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Run(); err != nil {
		log.Error("Artifactory proxy tests failed, full report available at the following path:", f.Name())
		t.Error(err)
	}
	log.Info("Full Artifactory proxy testing report available at the following path:", f.Name())
}

// Should be run only by @TestArtifactoryProxy test.
func TestArtifactoryHttpProxyEnvironmentVariableDelegator(t *testing.T) {
	testArtifactoryProxy(t, false)
}

// Should be run only by @TestArtifactoryProxy test.
func TestArtifactoryHttpsProxyEnvironmentVariableDelegator(t *testing.T) {
	testArtifactoryProxy(t, true)
}

func testArtifactoryProxy(t *testing.T, isHttps bool) {
	if !*tests.TestArtifactoryProxy {
		t.SkipNow()
	}
	authenticate()
	proxyRtUrl := prepareArtifactoryUrlForProxyTest(t)
	spec := spec.NewBuilder().Pattern(tests.Repo1 + "/*.zip").Recursive(true).BuildSpec()
	artifactoryDetails.Url = proxyRtUrl
	checkForErrDueToMissingProxy(spec, t)
	var port string
	if isHttps {
		go cliproxy.StartHttpsProxy()
		port = cliproxy.GetProxyHttpsPort()
	} else {
		go cliproxy.StartHttpProxy()
		port = cliproxy.GetProxyHttpPort()
	}
	// Let's wait for the reverse proxy to start up.
	err := checkIfServerIsUp(port, "http")
	if err != nil {
		t.Error(err)
	}
	searchCmd := generic.NewSearchCommand()
	searchCmd.SetRtDetails(artifactoryDetails).SetSpec(spec)
	err = searchCmd.Search()
	if err != nil {
		t.Error(err)
	}
	artifactoryDetails.Url = artAuth.GetUrl()
}

func prepareArtifactoryUrlForProxyTest(t *testing.T) string {
	rtUrl, err := url.Parse(artifactoryDetails.Url)
	if err != nil {
		t.Error(err)
	}
	rtHost, port, err := net.SplitHostPort(rtUrl.Host)
	if rtHost == "localhost" || rtHost == "127.0.0.1" {
		externalIp, err := getExternalIP()
		if err != nil {
			t.Error(err)
		}
		rtUrl.Host = externalIp + ":" + port
	}
	return rtUrl.String()
}

func checkForErrDueToMissingProxy(spec *spec.SpecFiles, t *testing.T) {
	searchCmd := generic.NewSearchCommand()
	searchCmd.SetRtDetails(artifactoryDetails).SetSpec(spec)
	err := searchCmd.Search()
	_, isUrlErr := err.(*url.Error)
	if err == nil || !isUrlErr {
		t.Error("Expected the request to fails, since the proxy is down.", err)
	}
}

func checkIfServerIsUp(port, proxyScheme string) error {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}

	for attempt := 0; attempt < 10; attempt++ {
		log.Info("Checking if proxy server is up and running.", strconv.Itoa(attempt+1), "attempt.", "URL:", proxyScheme+"://localhost:"+port)
		resp, err := client.Get(proxyScheme + "://localhost:" + port)
		if err != nil {
			attempt++
			time.Sleep(time.Second)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}

		return nil
	}
	return fmt.Errorf("Failed while waiting for the proxy server to be accessible.")
}

func TestXrayScanBuild(t *testing.T) {
	initArtifactoryTest(t)
	xrayServerPort := xray.StartXrayMockServer()
	serverUrl := "--url=http://localhost:" + strconv.Itoa(xrayServerPort)
	artifactoryCommandExecutor := tests.NewJfrogCli(execMain, "jfrog rt", serverUrl+getArtifactoryTestCredentials())
	artifactoryCommandExecutor.Exec("build-scan", xray.CleanScanBuildName, "3")

	cleanArtifactoryTest()
}

func TestArtifactorySetProperties(t *testing.T) {
	initArtifactoryTest(t)
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/a.in")
	artifactoryCli.Exec("sp", tests.Repo1+"/a.*", "prop=val")
	resultItems := searchItemsInArtifacotry(t)
	if len(resultItems) == 0 {
		t.Error("No artifacts were found.")
	}
	for _, item := range resultItems {
		properties := item.Properties
		if len(properties) < 1 {
			t.Error("Failed setting properties on item:", item.GetItemRelativePath())
		}
		for i, prop := range properties {
			if i > 0 {
				t.Error("Expected single property.")
			}
			if prop.Key != "prop" || prop.Value != "val" {
				t.Error("Wrong properties")
			}
		}
	}
	cleanArtifactoryTest()
}

func TestArtifactorySetPropertiesExcludeByCli(t *testing.T) {
	initArtifactoryTest(t)
	artifactoryCli.Exec("upload", "testsdata/a/a*.in", tests.Repo1+"/")
	artifactoryCli.Exec("sp", tests.Repo1+"/*", "prop=val", "--exclude-patterns=*a1.in;*a2.in")
	resultItems := searchItemsInArtifacotry(t)
	if len(resultItems) == 0 {
		t.Error("No artifacts were found.")
	}
	for _, item := range resultItems {
		if item.Name != "a3.in" {
			continue
		}
		properties := item.Properties
		if len(properties) < 1 {
			t.Error("Failed setting properties on item:", item.GetItemRelativePath())
		}
		for i, prop := range properties {
			if i > 0 {
				t.Error("Expected single property.")
			}
			if prop.Key != "prop" || prop.Value != "val" {
				t.Error("Wrong properties")
			}
		}
	}
	cleanArtifactoryTest()
}

func TestArtifactoryDeleteProperties(t *testing.T) {
	initArtifactoryTest(t)
	artifactoryCli.Exec("upload", "testsdata/a/a*.in", tests.Repo1+"/")
	artifactoryCli.Exec("sp", tests.Repo1+"/*", "prop=val;key=value")
	artifactoryCli.Exec("delp", tests.Repo1+"/*", "prop")
	resultItems := searchItemsInArtifacotry(t)
	if len(resultItems) == 0 {
		t.Error("No artifacts were found.")
	}

	for _, item := range resultItems {
		properties := item.Properties
		for _, prop := range properties {
			if prop.Key == "prop" {
				t.Error("Property 'prop' was not deleted from artifact", item.Name)
			}
		}
	}
	cleanArtifactoryTest()
}

func TestArtifactoryDeletePropertiesWithExclude(t *testing.T) {
	initArtifactoryTest(t)
	artifactoryCli.Exec("upload", "testsdata/a/a*.in", tests.Repo1+"/")
	artifactoryCli.Exec("sp", tests.Repo1+"/*", "prop=val")

	artifactoryCli.Exec("delp", tests.Repo1+"/*", "prop", "--exclude-patterns=*a1.in;*a2.in")
	resultItems := searchItemsInArtifacotry(t)
	if len(resultItems) == 0 {
		t.Error("No artifacts were found.")
	}

	for _, item := range resultItems {
		properties := item.Properties
		for _, prop := range properties {
			if item.Name == "a1.in" || item.Name == "a2.in" {
				// Check that properties were not removed.
				if prop.Key != "prop" || prop.Value != "val" {
					t.Error("Wrong properties")
				}
			}
		}
	}
	cleanArtifactoryTest()
}

func TestArtifactoryUploadFromHomeDir(t *testing.T) {
	initArtifactoryTest(t)
	testFileRel, testFileAbs := createFileInHomeDir(t, "cliTestFile.txt")
	artifactoryCli.Exec("upload", testFileRel, tests.Repo1, "--recursive=false")
	searchTxtPath, err := tests.CreateSpec(tests.SearchTxt)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetTxtUploadExpectedRepo1(), searchTxtPath, t)
	os.Remove(testFileAbs)
	cleanArtifactoryTest()
}

func createFileInHomeDir(t *testing.T, fileName string) (testFileRelPath string, testFileAbsPath string) {
	testFileRelPath = filepath.Join("~", fileName)
	testFileAbsPath = filepath.Join(fileutils.GetHomeDir(), fileName)
	d1 := []byte("test file")
	err := ioutil.WriteFile(testFileAbsPath, d1, 0644)
	if err != nil {
		t.Error("Couldn't create file:", err)
	}
	return
}

func TestArtifactoryUploadExcludeByCli1Wildcard(t *testing.T) {
	initArtifactoryTest(t)
	// Upload files
	artifactoryCli.Exec("upload", "testsdata/a/a*", tests.Repo1, "--exclude-patterns=*a2*;*a3.in")
	searchFilePath, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetSimpleUploadSpecialCharNoRegexExpectedRepo1(), searchFilePath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryUploadExcludeByCli1Regex(t *testing.T) {
	initArtifactoryTest(t)
	// Upload files
	artifactoryCli.Exec("upload", "testsdata/a/a(.*)", tests.Repo1, "--exclude-patterns=(.*)a2.*;.*a3.in", "--regexp=true")
	searchFilePath, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetSimpleUploadSpecialCharNoRegexExpectedRepo1(), searchFilePath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryUploadExcludeByCli2Wildcard(t *testing.T) {
	initArtifactoryTest(t)

	// Create temp dir
	absDirPath, err := ioutil.TempDir("", "cliTestDir")
	if err != nil {
		t.Error("Couldn't create dir:", err)
	}
	defer os.Remove(absDirPath)

	// Create temp files
	d1 := []byte("test file")
	err = ioutil.WriteFile(filepath.Join(absDirPath, "cliTestFile1.in"), d1, 0644)
	if err != nil {
		t.Error("Couldn't create file:", err)
	}
	err = ioutil.WriteFile(filepath.Join(absDirPath, "cliTestFile2.in"), d1, 0644)
	if err != nil {
		t.Error("Couldn't create file:", err)
	}

	// Upload files
	artifactoryCli.Exec("upload", filepath.ToSlash(absDirPath)+"/*", tests.Repo1, "--exclude-patterns=*cliTestFile1*")

	// Check files exists in artifactory
	searchFilePath, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory([]string{tests.Repo1 + "/cliTestFile2.in"}, searchFilePath, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryUploadExcludeByCli2Regex(t *testing.T) {
	initArtifactoryTest(t)

	// Create temp dir
	absDirPath, err := ioutil.TempDir("", "cliTestDir")
	if err != nil {
		t.Error("Couldn't create dir:", err)
	}
	defer os.Remove(absDirPath)

	// Create temp files
	d1 := []byte("test file")
	err = ioutil.WriteFile(filepath.Join(absDirPath, "cliTestFile1.in"), d1, 0644)
	if err != nil {
		t.Error("Couldn't create file:", err)
	}
	err = ioutil.WriteFile(filepath.Join(absDirPath, "cliTestFile2.in"), d1, 0644)
	if err != nil {
		t.Error("Couldn't create file:", err)
	}

	// Upload files
	artifactoryCli.Exec("upload", filepath.ToSlash(absDirPath)+"(.*)", tests.Repo1, "--exclude-patterns=(.*c)liTestFile1.*", "--regexp=true")

	// Check files exists in artifactory
	searchFilePath, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory([]string{tests.Repo1 + "/cliTestFile2.in"}, searchFilePath, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryUploadExcludeBySpecWildcard(t *testing.T) {
	initArtifactoryTest(t)

	// Upload files
	specFile, err := tests.CreateSpec(tests.UploadSpecExclude)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFile)

	// Validate files are moved by build number
	searchFilePath, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetUploadSpecExcludeRepo1(), searchFilePath, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryUploadExcludeBySpecRegex(t *testing.T) {
	initArtifactoryTest(t)

	// Upload files
	specFile, err := tests.CreateSpec(tests.UploadSpecExcludeRegex)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFile)

	// Validate files are moved by build number
	searchFilePath, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetUploadSpecExcludeRepo1(), searchFilePath, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryUploadWithRegexEscaping(t *testing.T) {
	initArtifactoryTest(t)
	// Upload files
	artifactoryCli.Exec("upload", "testsdata/regexp"+"(.*)"+"\\."+".*", tests.Repo1, "--regexp=true")
	searchFilePath, err := tests.CreateSpec(tests.SearchAllRepo1)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory([]string{tests.Repo1 + "/has.dot"}, searchFilePath, t)
	cleanArtifactoryTest()
}

func TestArtifactoryCopySpec(t *testing.T) {
	initArtifactoryTest(t)
	prepUploadFiles()
	err := prepCopyFiles()
	if err != nil {
		t.Error(err)
	}
	specFile, err := tests.CreateSpec(tests.MoveCopyDeleteSpec)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("copy", "--spec="+specFile)

	searchMoveDeleteSpecPath, err := tests.CreateSpec(tests.SearchMoveDeleteRepoSpec)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetMassiveMoveExpected(), searchMoveDeleteSpecPath, t)
	cleanArtifactoryTest()
}

// Upload symlink by full path to Artifactory and the link content checksum
// Download the symlink which was uploaded.
// validate the symlink content checksum
func TestValidateValidSymlink(t *testing.T) {
	if cliutils.IsWindows() {
		return
	}
	initArtifactoryTest(t)
	// Path to local file
	localFile := filepath.Join(tests.GetTestResourcesPath()+"a", "a1.in")
	// Path to valid symLink
	validLink := filepath.Join(tests.GetTestResourcesPath()+"a", "link")

	// Link valid symLink to local file
	err := os.Symlink(localFile, validLink)
	if err != nil {
		t.Error(err.Error())
	}

	// Upload symlink to artifactory
	artifactoryCli.Exec("u", validLink+" "+tests.Repo1+" --symlinks=true")

	// Delete the local symlink
	err = os.Remove(validLink)
	if err != nil {
		t.Error(err.Error())
	}

	// Download symlink from artifactory
	artifactoryCli.Exec("dl", tests.Repo1+"/link "+tests.GetTestResourcesPath()+"a/ --validate-symlinks=true")

	// Should be valid if successful
	validateSymLink(validLink, localFile, t)

	// Delete symlink and clean
	os.Remove(validLink)
	cleanArtifactoryTest()
}

// Upload symlink by full path to Artifactory and the link content checksum
// Download the symlink which was uploaded.
// validate the symlink content checksum.
func TestValidateBrokenSymlink(t *testing.T) {
	if cliutils.IsWindows() {
		return
	}
	initArtifactoryTest(t)

	// Path to broken symLink
	brokenLink := filepath.Join(tests.GetTestResourcesPath()+"a/", "brokenLink")

	// Link broken symLink to non_existing_path
	err := os.Symlink("non-non_existing_path-path", brokenLink)
	if err != nil {
		t.Error(err.Error())
	}

	// Upload symlink to artifactory
	artifactoryCli.Exec("u", brokenLink+" "+tests.Repo1+" --symlinks=true")

	// Delete the local symlink
	err = os.Remove(brokenLink)
	if err != nil {
		t.Error(err.Error())
	}

	// Try downloading symlink from artifactory. Since the link is broken, it shouldn't be downloaded
	artifactoryCli.Exec("dl", tests.Repo1+"/link "+tests.GetTestResourcesPath()+"a/ --validate-symlinks=true")
	if fileutils.IsPathExists(brokenLink, true) {
		os.Remove(brokenLink)
		t.Error("A broken symLink was downloaded although validate-symlinks flag was set to true")
	}

	// Clean
	cleanArtifactoryTest()
}

// Testing exclude pattern with symlinks.
// This test should not upload any files.
func TestExcludeBrokenSymlink(t *testing.T) {
	if cliutils.IsWindows() {
		return
	}
	initArtifactoryTest(t)

	// Creating broken symlink
	os.Mkdir(tests.Out, 0777)
	linkToNonExistingPath := filepath.Join(tests.Out, "link_to_non_existing_path")
	err := os.Symlink("non_existing_path", linkToNonExistingPath)
	if err != nil {
		t.Error(err.Error())
	}

	// This command should succeed because all artifacts are excluded.
	artifactoryCli.Exec("u", filepath.Join(tests.Out, "*"), tests.Repo1, "--symlinks=true", "--exclude-patterns=*")
	cleanArtifactoryTest()
}

// Upload symlink to Artifactory using wildcard pattern and the link content checksum
// Download the symlink which was uploaded.
// validate the symlink content checksum.
func TestSymlinkWildcardPathHandling(t *testing.T) {
	if cliutils.IsWindows() {
		return
	}
	initArtifactoryTest(t)
	localFile := filepath.Join(tests.GetTestResourcesPath()+"a/", "a1.in")
	link := filepath.Join(tests.GetTestResourcesPath()+"a/", "link")
	err := os.Symlink(localFile, link)
	if err != nil {
		t.Error(err.Error())
	}
	link1 := filepath.Join(tests.GetTestResourcesPath()+"a/", "link*")
	artifactoryCli.Exec("u", link1+" "+tests.Repo1+" --symlinks=true")
	err = os.Remove(link)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("dl", tests.Repo1+"/link "+tests.GetTestResourcesPath()+"a/ --validate-symlinks=true")
	validateSymLink(link, localFile, t)
	os.Remove(link)
	cleanArtifactoryTest()
}

// Upload symlink pointing to directory to Artifactory.
// Download the symlink which was uploaded.
func TestSymlinkToDirHandling(t *testing.T) {
	if cliutils.IsWindows() {
		return
	}
	initArtifactoryTest(t)
	localFile := filepath.Join(tests.GetTestResourcesPath(), "a")
	link := filepath.Join(tests.GetTestResourcesPath()+"a/", "link")
	err := os.Symlink(localFile, link)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("u", link+" "+tests.Repo1+" --symlinks=true --recursive=true")
	err = os.Remove(link)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("dl", tests.Repo1+"/link "+tests.GetTestResourcesPath()+"a/")
	validateSymLink(link, localFile, t)
	os.Remove(link)
	cleanArtifactoryTest()
}

// Upload symlink pointing to directory using wildcard path to Artifactory.
// Download the symlink which was uploaded.
func TestSymlinkToDirWildcardHandling(t *testing.T) {
	if cliutils.IsWindows() {
		return
	}
	initArtifactoryTest(t)
	localFile := filepath.Join(tests.GetTestResourcesPath(), "a")
	link := filepath.Join(tests.GetTestResourcesPath()+"a/", "link")
	err := os.Symlink(localFile, link)
	if err != nil {
		t.Error(err.Error())
	}
	link1 := filepath.Join(tests.GetTestResourcesPath()+"a/", "lin*")
	artifactoryCli.Exec("u", link1+" "+tests.Repo1+" --symlinks=true --recursive=true")
	err = os.Remove(link)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("dl", tests.Repo1+"/link "+tests.GetTestResourcesPath()+"a/")
	validateSymLink(link, localFile, t)
	os.Remove(link)
	cleanArtifactoryTest()
}

// Upload symlink pointing to directory using wildcard path to Artifactory.
// Download the symlink which was uploaded.
// The test create circular links and the test suppose to prune the circular searching.
func TestSymlinkInsideSymlinkDirWithRecursionIssueUpload(t *testing.T) {
	if cliutils.IsWindows() {
		return
	}
	initArtifactoryTest(t)
	localDirPath := filepath.Join(tests.GetTestResourcesPath(), "a")
	link1 := filepath.Join(tests.GetTestResourcesPath()+"a/", "link1")
	err := os.Symlink(localDirPath, link1)
	if err != nil {
		t.Error(err.Error())
	}
	localFilePath := filepath.Join(tests.GetTestResourcesPath()+"a/", "a1.in")
	link2 := filepath.Join(tests.GetTestResourcesPath()+"a/", "link2")
	err = os.Symlink(localFilePath, link2)
	if err != nil {
		t.Error(err.Error())
	}

	artifactoryCli.Exec("u", localDirPath+"/link* "+tests.Repo1+" --symlinks=true --recursive=true")
	err = os.Remove(link1)
	if err != nil {
		t.Error(err.Error())
	}

	err = os.Remove(link2)
	if err != nil {
		t.Error(err.Error())
	}

	artifactoryCli.Exec("dl", tests.Repo1+"/link* "+tests.GetTestResourcesPath()+"a/")
	validateSymLink(link1, localDirPath, t)
	os.Remove(link1)
	validateSymLink(link2, localFilePath, t)
	os.Remove(link2)
	cleanArtifactoryTest()
}

func validateSymLink(localLinkPath, localFilePath string, t *testing.T) {
	exists := fileutils.IsPathSymlink(localLinkPath)
	if !exists {
		t.Error(errors.New("Faild to download symlinks from artifactory"))
	}
	symlinks, err := filepath.EvalSymlinks(localLinkPath)
	if err != nil {
		t.Error(errors.New("Can't eval symlinks"))
	}
	if symlinks != localFilePath {
		t.Error(errors.New("Symlinks wasn't created as expected. expected:" + localFilePath + " actual: " + symlinks))
	}
}

func TestArtifactoryDelete(t *testing.T) {
	initArtifactoryTest(t)
	prepUploadFiles()
	err := prepCopyFiles()
	if err != nil {
		t.Error(err)
	}
	specFile, err := tests.CreateSpec(tests.MoveCopyDeleteSpec)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("copy", "--spec="+specFile)
	artifactoryCli.Exec("delete", tests.Repo2+"/nonflat_recursive_target/nonflat_recursive_source/a/b/*", "--quiet=true")

	searchMoveDeleteSpec, err := tests.CreateSpec(tests.SearchMoveDeleteRepoSpec)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetDelete1(), searchMoveDeleteSpec, t)
	cleanArtifactoryTest()
}

func TestArtifactoryDeleteFolderWithWildcard(t *testing.T) {
	initArtifactoryTest(t)
	prepUploadFiles()
	err := prepCopyFiles()
	if err != nil {
		t.Error(err)
	}

	specFile, err := tests.CreateSpec(tests.MoveCopyDeleteSpec)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("copy", "--spec="+specFile)

	client, err := httpclient.ClientBuilder().Build()
	if err != nil {
		t.Error(err)
	}
	resp, _, _, _ := client.SendGet(artifactoryDetails.Url+"api/storage/"+tests.Repo2+"/nonflat_recursive_target/nonflat_recursive_source/a/b/", true, artHttpDetails)
	if resp.StatusCode != http.StatusOK {
		t.Error("Missing folder in artifactory : " + tests.Repo2 + "/nonflat_recursive_target/nonflat_recursive_source/a/b/")
	}

	artifactoryCli.Exec("delete", tests.Repo2+"/nonflat_recursive_target/nonflat_recursive_source/*/b", "--quiet=true")
	resp, _, _, _ = client.SendGet(artifactoryDetails.Url+"api/storage/"+tests.Repo2+"/nonflat_recursive_target/nonflat_recursive_source/a/b/", true, artHttpDetails)
	if resp.StatusCode != http.StatusNotFound {
		t.Error("Couldn't delete folder in artifactory : " + tests.Repo2 + "/nonflat_recursive_target/nonflat_recursive_source/a/b/")
	}

	searchMoveDeleteSpec, err := tests.CreateSpec(tests.SearchMoveDeleteRepoSpec)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetDelete1(), searchMoveDeleteSpec, t)
	cleanArtifactoryTest()
}

func TestArtifactoryDeleteFolder(t *testing.T) {
	initArtifactoryTest(t)
	prepUploadFiles()
	artifactoryCli.Exec("delete", tests.Repo1+"/downloadTestResources", "--quiet=true")
	client, err := httpclient.ClientBuilder().Build()
	if err != nil {
		t.Error(err)
	}
	resp, body, _, err := client.SendGet(artifactoryDetails.Url+"api/storage/"+tests.Repo1+"/downloadTestResources", true, artHttpDetails)
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Error("Couldn't delete path: " + tests.Repo1 + "/downloadTestResources/ " + string(body))
	}

	cleanArtifactoryTest()
}

func TestArtifactoryDeleteFolderContent(t *testing.T) {
	initArtifactoryTest(t)
	prepUploadFiles()
	artifactoryCli.Exec("delete", tests.Repo1+"/downloadTestResources/", "--quiet=true")

	client, err := httpclient.ClientBuilder().Build()
	if err != nil {
		t.Error(err)
	}
	resp, body, _, err := client.SendGet(artifactoryDetails.Url+"api/storage/"+tests.Repo1+"/downloadTestResources", true, artHttpDetails)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Error("downloadTestResources shouldnn't be deleted: " + tests.Repo1 + "/downloadTestResources/ " + string(body))
	}
	folderContent, _, _, err := jsonparser.Get(body, "children")
	if err != nil {
		t.Error("Couldn't parse body:", string(body))
	}
	var folderChildren []struct{}
	err = json.Unmarshal(folderContent, &folderChildren)
	if err != nil {
		t.Error("Couldn't parse body:", string(body))
	}
	if len(folderChildren) != 0 {
		t.Error("downloadTestResources content wasn't deleted")
	}
	cleanArtifactoryTest()
}

func TestArtifactoryDeleteFoldersBySpec(t *testing.T) {
	initArtifactoryTest(t)
	prepUploadFiles()
	err := prepCopyFiles()
	if err != nil {
		t.Error(err)
	}

	deleteSpecPath, err := tests.CreateSpec(tests.DeleteSpec)
	if err != nil {
		t.Error(err)
	}

	artifactoryCli.Exec("delete", "--spec="+deleteSpecPath, "--quiet=true")

	client, err := httpclient.ClientBuilder().Build()
	if err != nil {
		t.Error(err)
	}
	resp, body, _, err := client.SendGet(artifactoryDetails.Url+"api/storage/"+tests.Repo1+"/downloadTestResources", true, artHttpDetails)
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Error("Couldn't delete path: " + tests.Repo1 + "/downloadTestResources/ " + string(body))
	}
	resp, body, _, err = client.SendGet(artifactoryDetails.Url+"api/storage/"+tests.Repo2+"/downloadTestResources", true, artHttpDetails)
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Error("Couldn't delete path: " + tests.Repo2 + "/downloadTestResources/ " + string(body))
	}

	cleanArtifactoryTest()
}

func TestArtifactoryDeleteExcludeByCli(t *testing.T) {
	initArtifactoryTest(t)

	// Upload files
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}

	artifactoryCli.Exec("upload", "--spec="+specFileA)
	artifactoryCli.Exec("upload", "--spec="+specFileB)

	// Delete by pattern
	artifactoryCli.Exec("del", tests.Repo1+"/data/", "--quiet=true", "--exclude-patterns=*b1.in;*b2.in;*b3.in;*c1.in")

	// Validate files are deleted
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetBuildDeleteExpected(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryDeleteExcludeBySpec(t *testing.T) {
	initArtifactoryTest(t)
	// Upload files
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA)
	artifactoryCli.Exec("upload", "--spec="+specFileB)

	specFile, err := tests.CreateSpec(tests.DelSpecExclude)
	if err != nil {
		t.Error(err)
	}

	// Delete by pattern
	artifactoryCli.Exec("del", "--spec="+specFile, "--quiet=true")

	// Validate files are deleted
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetBuildDeleteExpected(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryDisplayedPathToDelete(t *testing.T) {
	initArtifactoryTest(t)
	prepUploadFiles()
	err := prepCopyFiles()
	if err != nil {
		t.Error(err)
	}

	specFile, err := tests.CreateSpec(tests.DeleteComplexSpec)
	if err != nil {
		t.Error(err)
	}
	artifactsToDelete := getPathsToDelete(specFile)
	var displayedPaths []generic.SearchResult
	for _, v := range artifactsToDelete {
		displayedPaths = append(displayedPaths, generic.SearchResult{Path: v.GetItemRelativePath()})
	}

	tests.CompareExpectedVsActuals(tests.GetDeleteDisplyedFiles(), displayedPaths, t)
	cleanArtifactoryTest()
}

func TestArtifactoryDeleteBySpec(t *testing.T) {
	initArtifactoryTest(t)
	prepUploadFiles()
	err := prepCopyFiles()
	if err != nil {
		t.Error(err)
	}

	specFile, err := tests.CreateSpec(tests.DeleteComplexSpec)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("delete", "--spec="+specFile, "--quiet=true")

	artifactsToDelete := getPathsToDelete(specFile)
	if len(artifactsToDelete) != 0 {
		t.Error("Couldn't delete paths")
	}

	cleanArtifactoryTest()
}

func TestArtifactoryMassiveDownloadSpec(t *testing.T) {
	initArtifactoryTest(t)
	prepUploadFiles()
	specFile, err := tests.CreateSpec(tests.DownloadSpec)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("download", "--spec="+specFile)

	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	tests.IsExistLocally(tests.GetMassiveDownload(), paths, t)
	cleanArtifactoryTest()
}

func TestArtifactoryMassiveUploadSpec(t *testing.T) {
	initArtifactoryTest(t)
	specFile, err := tests.CreateSpec(tests.UploadSpec)
	if err != nil {
		t.Error(err)
	}
	resultSpecFile, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFile)

	isExistInArtifactory(tests.GetMassiveUpload(), resultSpecFile, t)
	isExistInArtifactoryByProps(tests.GetPropsExpected(), tests.Repo1+"/*/properties/*.in", "searchMe=true", t)
	cleanArtifactoryTest()
}

func TestArtifactoryFolderUploadRecursiveNonFlat(t *testing.T) {
	initArtifactoryTest(t)
	dirInnerPath := fileutils.GetFileSeparator() + "inner" + fileutils.GetFileSeparator() + "folder"
	canonicalPath := tests.Out + dirInnerPath

	err := os.MkdirAll(canonicalPath, 0777)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("upload", tests.Out+"/(*)", tests.Repo1+"/{1}/", "--include-dirs=true", "--recursive=true", "--flat=false")
	err = os.RemoveAll(tests.Out)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--include-dirs=true", "--recursive=true")
	expectedPath := []string{tests.Out, "inner", "folder", "out", "inner", "folder"}
	if !fileutils.IsPathExists(strings.Join(expectedPath, fileutils.GetFileSeparator()), false) {
		t.Error("Failed to download folders from Artifatory")
	}
	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryFlatFolderUpload(t *testing.T) {
	initArtifactoryTest(t)
	dirInnerPath := fileutils.GetFileSeparator() + "inner" + fileutils.GetFileSeparator() + "folder"
	canonicalPath := tests.Out + dirInnerPath
	err := os.MkdirAll(canonicalPath, 0777)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("upload", tests.Out+"/(*)", tests.Repo1+"/{1}/", "--include-dirs=true", "--flat=true")
	err = os.RemoveAll(tests.Out)
	if err != nil {
		t.Error(err.Error())
	}
	// Non flat download
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--include-dirs=true", "--recursive=true")
	if !fileutils.IsPathExists(canonicalPath+fileutils.GetFileSeparator()+"folder", false) {
		t.Error("Failed to download folders from Artifatory")
	}
	// Cleanup
	cleanArtifactoryTest()
}

// Test the definition of bottom chain directories  - are directories which do not include other directories which match the pattern
func TestArtifactoryIncludeDirFlatNonEmptyFolderUpload(t *testing.T) {
	initArtifactoryTest(t)
	// 'c' folder is defined as bottom chain directory therefor should be uploaded when using flat=true even though 'c' is not empty
	artifactoryCli.Exec("upload", tests.GetTestResourcesPath()+"*", tests.Repo1, "--include-dirs=true", "--flat=true")
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--include-dirs=true", "--recursive=true")
	if !fileutils.IsPathExists(tests.Out+"/c", false) {
		t.Error("Failed to download folders from Artifatory")
	}
	// Cleanup
	cleanArtifactoryTest()
}

// Test large file upload
func TestArtifactoryLargeFileDownload(t *testing.T) {
	initArtifactoryTest(t)
	tempFile, err := fileutils.CreateFilePath(tests.Out, "largeTempFile.txt")
	if err != nil {
		t.Error(err.Error())
	}
	MByte := 1024 * 1024
	randFile, err := io.CreateRandFile(tempFile, MByte*100)
	if err != nil {
		t.Error(err.Error())
	}
	defer randFile.File.Close()

	absTargetPath, err := filepath.Abs(tests.Out)
	if err != nil {
		t.Error(err.Error())
	}
	// Upload large file to Artifactory
	artifactoryCli.Exec("upload", tempFile, path.Join(tests.Repo1, "largeFilePath", "largeFile.txt"))

	// Download large file from Artifactory to absolute path
	artifactoryCli.Exec("download", tests.Repo1, absTargetPath+fileutils.GetFileSeparator(), "--flat=true")
	artifactoryCli.Exec("download", tests.Repo1, absTargetPath+fileutils.GetFileSeparator())

	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	expected := []string{
		filepath.Join(tests.Out, "largeFile.txt"),
		filepath.Join(tests.Out, "largeFilePath", "largeFile.txt"),
	}
	tests.IsExistLocally(expected, paths, t)

	// Cleanup
	cleanArtifactoryTest()
}

// Test the definition of bottom chain directories  - are directories which do not include other directories which match the pattern
func TestArtifactoryDownloadNotIncludeDirs(t *testing.T) {
	initArtifactoryTest(t)
	// 'c' folder is defined as bottom chain directory therefor should be uploaded when using flat=true even though 'c' is not empty
	artifactoryCli.Exec("upload", tests.GetTestResourcesPath()+"*/c", tests.Repo1, "--include-dirs=true", "--flat=true")
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--recursive=true")
	if fileutils.IsPathExists(tests.Out+"/c", false) {
		t.Error("Failed to download folders from Artifatory")
	}
	// Cleanup
	cleanArtifactoryTest()
}

// Test the definition of bottom chain directories  - are directories which do not include other directories which match the pattern
func TestArtifactoryDownloadFlatTrue(t *testing.T) {
	initArtifactoryTest(t)
	dirInnerPath := "empty" + fileutils.GetFileSeparator() + "folder"
	canonicalPath := tests.GetTestResourcesPath() + dirInnerPath
	err := os.MkdirAll(canonicalPath, 0777)
	if err != nil {
		t.Error(err.Error())
	}

	// 'c' folder is defined as bottom chain directory therefor should be uploaded when using flat=true even though 'c' is not empty
	artifactoryCli.Exec("upload", tests.GetTestResourcesPath()+"(*)/*", tests.Repo1+"/{1}/", "--include-dirs=true", "--flat=true")
	// Download without include-dirs
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--recursive=true", "--flat=true")
	if fileutils.IsPathExists(tests.Out+"/c", false) {
		t.Error("'c' folder shouldn't be exist.")
	}
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--include-dirs=true", "--recursive=true", "--flat=true")
	// Inner folder with files in it
	if !fileutils.IsPathExists(tests.Out+"/c", false) {
		t.Error("'c' folder should exist.")
	}
	// Empty inner folder
	if !fileutils.IsPathExists(tests.Out+"/folder", false) {
		t.Error("'folder' folder should exist.")
	}
	// Folder on root with files
	if !fileutils.IsPathExists(tests.Out+"/a$+~&^a#", false) {
		t.Error("'a$+~&^a#' folder should be exist.")
	}
	// None bottom directory - shouldn't exist.
	if fileutils.IsPathExists(tests.Out+"/a", false) {
		t.Error("'a' folder shouldn't be exist.")
	}
	// None bottom directory - shouldn't exist.
	if fileutils.IsPathExists(tests.Out+"/b", false) {
		t.Error("'b' folder shouldn't be exist.")
	}
	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryIncludeDirFlatNonEmptyFolderUploadMatchingPattern(t *testing.T) {
	initArtifactoryTest(t)
	// 'c' folder is defined as bottom chain directory therefor should be uploaded when using flat=true even though 'c' is not empty
	artifactoryCli.Exec("upload", tests.GetTestResourcesPath()+"*/c", tests.Repo1, "--include-dirs=true", "--flat=true")
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--include-dirs=true", "--recursive=true")
	if !fileutils.IsPathExists(tests.Out+"/c", false) {
		t.Error("Failed to download folders from Artifatory")
	}
	// Cleanup
	cleanArtifactoryTest()
}

// Test the definition of bottom chain directories  - are directories which do not include other directories which match the pattern
func TestArtifactoryUploadFlatFolderWithFileAndInnerEmptyMatchingPattern(t *testing.T) {
	initArtifactoryTest(t)
	path := tests.GetTestResourcesPath() + "a/b/c/d"
	err := os.MkdirAll(path, 0777)
	if err != nil {
		t.Error(err.Error())
	}
	// We created a empty child folder to 'c' therefor 'c' is not longer a bottom chain and new 'd' inner directory is indeed bottom chain directory.
	// 'd' should uploaded and 'c' shouldn't
	artifactoryCli.Exec("upload", tests.GetTestResourcesPath()+"*", tests.Repo1, "--include-dirs=true", "--flat=true")
	err = os.RemoveAll(path)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--include-dirs=true", "--recursive=true")
	if fileutils.IsPathExists(tests.Out+"/c", false) {
		t.Error("'c' folder shouldn't be exsit")
	}
	if !fileutils.IsPathExists(tests.Out+"/d", false) {
		t.Error("bottom chian directory, 'd', is missing")
	}
	// Cleanup
	cleanArtifactoryTest()
}

// Test the definition of bottom chain directories  - are directories which do not include other directories which match the pattern
func TestArtifactoryUploadFlatFolderWithFileAndInnerEmptyMatchingPatternWithPlaceHolders(t *testing.T) {
	initArtifactoryTest(t)
	relativePath := "/a/b/c/d"
	fullPath := tests.GetTestResourcesPath() + relativePath
	err := os.MkdirAll(fullPath, 0777)
	if err != nil {
		t.Error(err.Error())
	}
	// We created a empty child folder to 'c' therefor 'c' is not longer a bottom chain and new 'd' inner directory is indeed bottom chain directory.
	// 'd' should uploaded and 'c' shouldn't
	artifactoryCli.Exec("upload", tests.GetTestResourcesPath()+"(*)/*", tests.Repo1+"/{1}/", "--include-dirs=true", "--flat=true")
	err = os.RemoveAll(fullPath)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--include-dirs=true", "--recursive=true")
	if !fileutils.IsPathExists(tests.Out+relativePath, false) {
		t.Error("bottom chian directory, 'd', is missing")
	}

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryFlatFolderDownload1(t *testing.T) {
	initArtifactoryTest(t)
	dirInnerPath := fileutils.GetFileSeparator() + "inner" + fileutils.GetFileSeparator() + "folder"
	canonicalPath := tests.Out + dirInnerPath
	err := os.MkdirAll(canonicalPath, 0777)
	if err != nil {
		t.Error(err.Error())
	}
	// Flat true by default for upload, by using placeholder we indeed create folders hierarchy in Artifactory inner/folder/folder
	artifactoryCli.Exec("upload", tests.Out+"/(*)", tests.Repo1+"/{1}/", "--include-dirs=true")
	err = os.RemoveAll(tests.Out)
	if err != nil {
		t.Error(err.Error())
	}
	// Only the inner folder should be downland e.g 'folder'
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--include-dirs=true", "--flat=true")
	if !fileutils.IsPathExists(tests.Out+fileutils.GetFileSeparator()+"folder", false) && fileutils.IsPathExists(tests.Out+fileutils.GetFileSeparator()+"inner", false) {
		t.Error("Failed to download folders from Artifatory")
	}
	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryFolderUploadRecursiveUsingSpec(t *testing.T) {
	initArtifactoryTest(t)
	dirInnerPath := "empty" + fileutils.GetFileSeparator() + "folder"
	canonicalPath := tests.GetTestResourcesPath() + dirInnerPath
	err := os.MkdirAll(canonicalPath, 0777)
	if err != nil {
		t.Error(err)
	}
	specFile, err := tests.CreateSpec(tests.UploadEmptyDirs)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFile)

	specFile, err = tests.CreateSpec(tests.DownloadEmptyDirs)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("download", "--spec="+specFile)
	if !fileutils.IsPathExists(tests.Out+"/folder", false) {
		t.Error("Failed to download folders from Artifatory")
	}
	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryFolderUploadNonRecursive(t *testing.T) {
	initArtifactoryTest(t)
	canonicalPath := tests.Out + fileutils.GetFileSeparator() + "inner" + fileutils.GetFileSeparator() + "folder"
	err := os.MkdirAll(canonicalPath, 0777)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("upload", tests.Out+"/", tests.Repo1, "--recursive=true", "--include-dirs=true")
	err = os.RemoveAll(tests.Out)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("download", tests.Repo1, tests.Out+"/", "--include-dirs=true")
	if !fileutils.IsPathExists(tests.Out+"/folder", false) {
		t.Error("Failed to download folder from Artifatory")
	}
	if fileutils.IsPathExists(canonicalPath, false) {
		t.Error("Path should be flat ")
	}
	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryFolderDownloadNonRecursive(t *testing.T) {
	initArtifactoryTest(t)
	canonicalPath := tests.Out + fileutils.GetFileSeparator() + "inner" + fileutils.GetFileSeparator() + "folder"
	err := os.MkdirAll(canonicalPath, 0777)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("upload", tests.Out+"/", tests.Repo1, "--recursive=true", "--include-dirs=true", "--flat=false")
	err = os.RemoveAll(tests.Out)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("download", tests.Repo1+"/*", "--recursive=false", "--include-dirs=true")
	if !fileutils.IsPathExists(tests.Out, false) {
		t.Error("Failed to download folder from Artifatory")
	}
	if fileutils.IsPathExists(canonicalPath, false) {
		t.Error("Path should be flat. ")
	}
	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryChecksumDownload(t *testing.T) {
	initArtifactoryTest(t)

	var filePath = "testsdata/a/a1.in"
	artifactoryCli.Exec("upload", filePath, tests.Repo1)
	testChecksumDownload(t, "/a1.in")
	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryChecksumDownloadRenameFileName(t *testing.T) {
	initArtifactoryTest(t)

	var filePath = "testsdata/a/a1.in"
	artifactoryCli.Exec("upload", filePath, tests.Repo1)
	testChecksumDownload(t, "/a1.out")
	// Cleanup
	cleanArtifactoryTest()
}

func testChecksumDownload(t *testing.T, outFileName string) {
	artifactoryCli.Exec("download "+tests.Repo1+"/a1.in", tests.Out+outFileName)

	exists, err := fileutils.IsFileExists(tests.Out+outFileName, false)
	if err != nil {
		t.Error(err.Error())
	}
	if !exists {
		t.Error("Failed to download file from Artifatory")
	}

	firstFileInfo, _ := os.Stat(tests.Out + outFileName)
	firstDownloadTime := firstFileInfo.ModTime()

	artifactoryCli.Exec("download "+tests.Repo1+"/a1.in", tests.Out+outFileName)
	secondFileInfo, _ := os.Stat(tests.Out + outFileName)
	secondDownloadTime := secondFileInfo.ModTime()

	if !firstDownloadTime.Equal(secondDownloadTime) {
		t.Error("Checksum download failed, the file was downloaded twice")
	}
}

func TestArtifactoryDownloadByPatternAndBuildUsingSpec(t *testing.T) {
	initArtifactoryTest(t)
	buildName, buildNumberA, buildNumberB := "cli-test-build", "10", "11"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.BuildDownloadSpec)
	if err != nil {
		t.Error(err)
	}
	// Upload files with buildName and buildNumber
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--build-name="+buildName, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--build-name="+buildName, "--build-number="+buildNumberB)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildName, buildNumberA)
	artifactoryCli.Exec("build-publish", buildName, buildNumberB)

	// Download by build number
	artifactoryCli.Exec("download", "--spec="+specFile)

	// Validate files are downloaded by build number
	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetBuildDownload(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

func TestArtifactoryDownloadByBuildNoPatternUsingSpec(t *testing.T) {
	initArtifactoryTest(t)
	buildName, buildNumberA, buildNumberB := "cli-test-build", "10", "11"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.BuildDownloadSpecNoPattern)
	if err != nil {
		t.Error(err)
	}
	// Upload files with buildName and buildNumber
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--build-name="+buildName, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--build-name="+buildName, "--build-number="+buildNumberB)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildName, buildNumberA)
	artifactoryCli.Exec("build-publish", buildName, buildNumberB)

	// Download by build number
	artifactoryCli.Exec("download", "--spec="+specFile)

	// Validate files are downloaded by build number
	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetBuildDownload(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

// Upload a file to build A.
// Verify that it doesn't exist in B.
func TestArtifactoryDownloadArtifactDoesntExistInBuild(t *testing.T) {
	initArtifactoryTest(t)
	buildName, buildNumber := "cli-test-build1", "10"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.BuildDownloadSpecNoBuildNumber)
	if err != nil {
		t.Error(err)
	}
	// Upload a file
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/data/a10.in", "--build-name="+buildName, "--build-number="+buildNumber)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildName, buildNumber)

	// Download from different build number
	artifactoryCli.Exec("download", "--spec="+specFile)

	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetBuildDownloadDoesntExist(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

// Upload a file to 2 different builds.
// Verify that we don't download files with same sha and different build name and build number.
func TestArtifactoryDownloadByShaAndBuild(t *testing.T) {
	initArtifactoryTest(t)
	buildNameA, buildNameB, buildNumberA, buildNumberB, buildNumberC := "cli-test-build1", "cli-test-build2", "10", "11", "12"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameA, artHttpDetails)
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameB, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.BuildDownloadSpecNoBuildNumber)
	if err != nil {
		t.Error(err)
	}

	// Upload 3 similar files to 3 different builds
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/data/a10.in", "--build-name="+buildNameB, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/data/a11.in", "--build-name="+buildNameA, "--build-number="+buildNumberB)
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/data/a12.in", "--build-name="+buildNameA, "--build-number="+buildNumberC)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildNameB, buildNumberA)
	artifactoryCli.Exec("build-publish", buildNameA, buildNumberB)
	artifactoryCli.Exec("build-publish", buildNameA, buildNumberC)

	// Download by build number
	artifactoryCli.Exec("download", "--spec="+specFile)

	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetBuildDownloadByShaAndBuild(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameA, artHttpDetails)
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameB, artHttpDetails)
	cleanArtifactoryTest()
}

// Upload a file to 2 different builds.
// Verify that we don't download files with same sha and build name and different build number.
func TestArtifactoryDownloadByShaAndBuildName(t *testing.T) {
	initArtifactoryTest(t)
	buildNameA, buildNameB, buildNumberA, buildNumberB, buildNumberC := "cli-test-build1", "cli-test-build2", "10", "11", "12"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameA, artHttpDetails)
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameB, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.BuildDownloadSpecNoBuildNumber)
	if err != nil {
		t.Error(err)
	}

	// Upload 3 similar files to 2 different builds
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/data/a10.in", "--build-name="+buildNameB, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/data/a11.in", "--build-name="+buildNameB, "--build-number="+buildNumberB)
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/data/a12.in", "--build-name="+buildNameA, "--build-number="+buildNumberC)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildNameA, buildNumberC)
	artifactoryCli.Exec("build-publish", buildNameB, buildNumberA)
	artifactoryCli.Exec("build-publish", buildNameB, buildNumberB)

	// Download by build number
	artifactoryCli.Exec("download", "--spec="+specFile)

	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetBuildDownloadByShaAndBuildName(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameA, artHttpDetails)
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameB, artHttpDetails)
	cleanArtifactoryTest()
}

func TestArtifactoryDownloadByBuildUsingSimpleDownload(t *testing.T) {
	initArtifactoryTest(t)
	buildName, buildNumberA, buildNumberB := "cli-test-build", "10", "11"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)

	// Upload files with buildName and buildNumber
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--build-name="+buildName, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--build-name="+buildName, "--build-number="+buildNumberB)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildName, buildNumberA)
	artifactoryCli.Exec("build-publish", buildName, buildNumberB)

	// Download by build number, a1 should not be downloaded, b1 should
	artifactoryCli.Exec("download "+tests.Repo1+"/data/a1.in "+tests.Out+fileutils.GetFileSeparator()+"download"+fileutils.GetFileSeparator()+"simple_by_build"+fileutils.GetFileSeparator(), "--build="+buildName)
	artifactoryCli.Exec("download "+tests.Repo1+"/data/b1.in "+tests.Out+fileutils.GetFileSeparator()+"download"+fileutils.GetFileSeparator()+"simple_by_build"+fileutils.GetFileSeparator(), "--build="+buildName)

	// Validate files are downloaded by build number
	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetBuildSimpleDownload(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

func TestArtifactoryDownloadByBuildNoPatternUsingSimpleDownload(t *testing.T) {
	initArtifactoryTest(t)
	buildName, buildNumberA, buildNumberB := "cli-test-build", "10", "11"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)

	// Upload files with buildName and buildNumber
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}

	artifactoryCli.Exec("upload", "--spec="+specFileA, "--build-name="+buildName, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--build-name="+buildName, "--build-number="+buildNumberB)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildName, buildNumberA)
	artifactoryCli.Exec("build-publish", buildName, buildNumberB)

	// Download by build number, a1 should not be downloaded, b1 should
	artifactoryCli.Exec("download * "+tests.Out+fileutils.GetFileSeparator()+"download"+fileutils.GetFileSeparator()+"simple_by_build"+fileutils.GetFileSeparator(), "--build="+buildName+"/"+buildNumberA)

	// Validate files are downloaded by build number
	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetBuildSimpleDownloadNoPattern(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

func TestArtifactoryDownloadByArchiveEntriesCli(t *testing.T) {
	initArtifactoryTest(t)
	uploadSpecFile, err := tests.CreateSpec(tests.ArchiveEntriesUpload)
	if err != nil {
		t.Error(err)
	}

	// Upload archives
	artifactoryCli.Exec("upload", "--spec="+uploadSpecFile)

	// Create executor for running with retries
	retryExecutor := createRetryExecutorForArchiveEntries(tests.GetBuildArchiveEntriesDownloadCli(),
		[]string{"dl", tests.Repo1, "out/", "--archive-entries=(*)c1.in", "--flat=true"})

	// Perform download by archive-entries only the archives containing c1.in, and validate results
	if err = retryExecutor.Execute(); err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryDownloadByArchiveEntriesSpecificPathCli(t *testing.T) {
	initArtifactoryTest(t)
	uploadSpecFile, err := tests.CreateSpec(tests.ArchiveEntriesUpload)
	if err != nil {
		t.Error(err)
	}

	// Upload archives
	artifactoryCli.Exec("upload", "--spec="+uploadSpecFile)

	// Create executor for running with retries
	retryExecutor := createRetryExecutorForArchiveEntries(tests.GetBuildArchiveEntriesSpecificPathDownload(),
		[]string{"dl", tests.Repo1, "out/", "--archive-entries=b/c/c1.in", "--flat=true"})

	// Perform download by archive-entries only the archives containing c1.in, and validate results
	if err = retryExecutor.Execute(); err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryDownloadByArchiveEntriesSpec(t *testing.T) {
	initArtifactoryTest(t)
	uploadSpecFile, err := tests.CreateSpec(tests.ArchiveEntriesUpload)
	if err != nil {
		t.Error(err)
	}
	downloadSpecFile, err := tests.CreateSpec(tests.ArchiveEntriesDownload)
	if err != nil {
		t.Error(err)
	}

	// Upload archives
	artifactoryCli.Exec("upload", "--spec="+uploadSpecFile)

	// Create executor for running with retries
	retryExecutor := createRetryExecutorForArchiveEntries(tests.GetBuildArchiveEntriesDownloadSpec(),
		[]string{"dl", "--spec=" + downloadSpecFile})

	// Perform download by archive-entries only the archives containing d1.in, and validate results
	if err = retryExecutor.Execute(); err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	cleanArtifactoryTest()
}

func createRetryExecutorForArchiveEntries(expected []string, args []string) *clientutils.RetryExecutor {
	return &clientutils.RetryExecutor{
		MaxRetries:      120,
		RetriesInterval: 1,
		ErrorMessage:    "Waiting for Artifactory to index archives...",
		ExecutionHandler: func() (bool, error) {
			err := validateDownloadByArchiveEntries(expected, args)
			if err != nil {
				return true, err
			}

			return false, nil
		},
	}
}

func validateDownloadByArchiveEntries(expected []string, args []string) error {
	// Execute the requested cli command
	artifactoryCli.Exec(args...)

	// Validate files are downloaded as expected
	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	return tests.ValidateListsIdentical(expected, paths)
}

func TestArtifactoryDownloadExcludeByCli(t *testing.T) {
	initArtifactoryTest(t)

	// Upload files
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--recursive=true")
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--recursive=true")

	// Download by pattern
	artifactoryCli.Exec("download", tests.Repo1+" out/download/aql_by_artifacts/", "--exclude-patterns=*/a1.in;*a2.*;data/c2.in")

	// Validate files are excluded
	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetBuildExcludeDownload(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryDownloadExcludeBySpec(t *testing.T) {
	initArtifactoryTest(t)
	specFile, err := tests.CreateSpec(tests.DownloadSpecExclude)
	if err != nil {
		t.Error(err)
	}

	// Upload files
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--recursive=true", "--flat=false")
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--recursive=true", "--flat=false")

	// Download by spec
	artifactoryCli.Exec("download", "--spec="+specFile)

	// Validate files are excluded
	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetBuildExcludeDownloadBySpec(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryDownloadExcludeBySpecOverride(t *testing.T) {
	initArtifactoryTest(t)

	// Upload files with buildName and buildNumber
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--recursive=true", "--flat=false")
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--recursive=true", "--flat=false")

	// Download by spec
	specFile, err := tests.CreateSpec(tests.DownloadSpecExclude)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("download", "--spec="+specFile, "--exclude-patterns=*a1.in;*a2.in;*c2.in")

	// Validate files are downloaded by build number
	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetBuildExcludeDownload(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	cleanArtifactoryTest()
}

// Sort and limit changes the way properties are used so this should be tested with symlinks and search by build

// Upload symlink by full path to Artifactory and the link content checksum
// Download the symlink which was uploaded with limit param.
// validate the symlink content checksum.
func TestArtifactoryLimitWithSymlink(t *testing.T) {
	if cliutils.IsWindows() {
		return
	}
	initArtifactoryTest(t)
	localFile := filepath.Join(tests.GetTestResourcesPath()+"a", "a1.in")
	link := filepath.Join(tests.GetTestResourcesPath()+"a", "link")
	err := os.Symlink(localFile, link)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("u", link+" "+tests.Repo1+" --symlinks=true")
	err = os.Remove(link)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("dl", tests.Repo1+"/link "+tests.GetTestResourcesPath()+"a/ --validate-symlinks=true --limit=1")
	validateSortLimitWithSymLink(link, localFile, t)
	os.Remove(link)
	cleanArtifactoryTest()
}

// Upload symlink by full path to Artifactory and the link content checksum
// Download the symlink which was uploaded with limit param.
// validate the symlink content checksum.
func TestArtifactorySortWithSymlink(t *testing.T) {
	if cliutils.IsWindows() {
		return
	}
	initArtifactoryTest(t)
	localFile := filepath.Join(tests.GetTestResourcesPath()+"a", "a1.in")
	link := filepath.Join(tests.GetTestResourcesPath()+"a", "link")
	err := os.Symlink(localFile, link)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("u", link+" "+tests.Repo1+" --symlinks=true")
	err = os.Remove(link)
	if err != nil {
		t.Error(err.Error())
	}
	artifactoryCli.Exec("dl", tests.Repo1+"/link "+tests.GetTestResourcesPath()+"a/ --validate-symlinks=true --sort-by=created")
	validateSortLimitWithSymLink(link, localFile, t)
	os.Remove(link)
	cleanArtifactoryTest()
}

func validateSortLimitWithSymLink(localLinkPath, localFilePath string, t *testing.T) {
	exists := fileutils.IsPathSymlink(localLinkPath)
	if !exists {
		t.Error(errors.New("Faild to download symlinks from artifactory with Sort/Limit flag"))
	}
	symlinks, err := filepath.EvalSymlinks(localLinkPath)
	if err != nil {
		t.Error(errors.New("Can't eval symlinks with Sort/Limit flag"))
	}
	if symlinks != localFilePath {
		t.Error(errors.New("Symlinks wasn't created as expected with Sort/Limit flag. expected:" + localFilePath + " actual: " + symlinks))
	}
}

// Upload a file to 2 different builds.
// Verify that we don't download files with same sha and build name and different build number when sort is configured.
func TestArtifactoryDownloadByShaAndBuildNameWithSort(t *testing.T) {
	initArtifactoryTest(t)
	buildNameA, buildNameB, buildNumberA, buildNumberB, buildNumberC := "cli-test-build1", "cli-test-build2", "10", "11", "12"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameA, artHttpDetails)
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameB, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.BuildDownloadSpecNoBuildNumberWithSort)
	if err != nil {
		t.Error(err)
	}
	// Upload 3 similar files to 2 different builds
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/data/a10.in", "--build-name="+buildNameB, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/data/a11.in", "--build-name="+buildNameB, "--build-number="+buildNumberB)
	artifactoryCli.Exec("upload", "testsdata/a/a1.in", tests.Repo1+"/data/a12.in", "--build-name="+buildNameA, "--build-number="+buildNumberC)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildNameA, buildNumberC)
	artifactoryCli.Exec("build-publish", buildNameB, buildNumberA)
	artifactoryCli.Exec("build-publish", buildNameB, buildNumberB)

	// Download by build number
	artifactoryCli.Exec("download", "--sort-by=created --spec="+specFile)

	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(filepath.Join(tests.Out, "download", "sort_limit_by_build"), false)
	err = tests.ValidateListsIdentical(tests.GetBuildDownloadByShaAndBuildNameWithSort(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameA, artHttpDetails)
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildNameB, artHttpDetails)
	cleanArtifactoryTest()
}

func TestArtifactoryCopyByBuildUsingSpec(t *testing.T) {
	initArtifactoryTest(t)
	buildName, buildNumberA, buildNumberB := "cli-test-build", "10", "11"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.CopyByBuildSpec)
	if err != nil {
		t.Error(err)
	}
	// Upload files with buildName and buildNumber: a* uploaded with build number "10", b* uploaded with build number "11"
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--build-name="+buildName, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--build-name="+buildName, "--build-number="+buildNumberB)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildName, buildNumberA)
	artifactoryCli.Exec("build-publish", buildName, buildNumberB)

	// Copy by build name "cli-test-build" and build number "10" from spec, a* should be copied
	artifactoryCli.Exec("copy", "--spec="+specFile)

	// Validate files are Copied by build number
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetBuildCopyExpected(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

func TestArtifactoryCopyByBuildPatternAllUsingSpec(t *testing.T) {
	initArtifactoryTest(t)
	buildName, buildNumberA, buildNumberB := "cli-test-build", "10", "11"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.CopyByBuildPatternAllSpec)
	if err != nil {
		t.Error(err)
	}
	// Upload files with buildName and buildNumber: a* uploaded with build number "10", b* uploaded with build number "11"
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--build-name="+buildName, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--build-name="+buildName, "--build-number="+buildNumberB)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildName, buildNumberA)
	artifactoryCli.Exec("build-publish", buildName, buildNumberB)

	// Copy by build name "cli-test-build" and build number "10" from spec, a* should be copied
	artifactoryCli.Exec("copy", "--spec="+specFile)

	// Validate files are Copied by build number
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetBuildCopyExpected(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

func TestArtifactorySortAndLimit(t *testing.T) {
	initArtifactoryTest(t)

	// Upload all testdata/a/ files
	artifactoryCli.Exec("upload", "testsdata/a/(*)", tests.Repo1+"/data/{1}")

	// Download 1 sorted by name asc
	artifactoryCli.Exec("download", tests.Repo1+"/data/ out/download/sort_limit/", "--sort-by=name", "--limit=1")

	// Download 3 sorted by depth desc
	artifactoryCli.Exec("download", tests.Repo1+"/data/ out/download/sort_limit/", "--sort-by=depth", "--limit=3", "--sort-order=desc")

	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err := tests.ValidateListsIdentical(tests.GetSortAndLimit(), paths)
	if err != nil {
		t.Error(err.Error())
	}

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryOffset(t *testing.T) {
	initArtifactoryTest(t)

	// Upload all testdata/a/ files
	artifactoryCli.Exec("upload", "testsdata/a/*", path.Join(tests.Repo1, "offset_test")+"/", "--flat=true")

	// Downloading files one by one, to check that the offset is working as expected.
	// Download only the first file, expecting to download a1.in
	artifactoryCli.Exec("download", tests.Repo1+"/offset_test/", tests.Out+"/", "--flat=true", "--sort-by=name", "--limit=1", "--offset=0")
	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	tests.IsExistLocally([]string{tests.Out + fileutils.GetFileSeparator() + "a1.in"}, paths, t)

	// Download the second file, expecting to download a2.in
	artifactoryCli.Exec("download", tests.Repo1+"/offset_test/", tests.Out+"/", "--flat=true", "--sort-by=name", "--limit=1", "--offset=1")
	paths, _ = fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	tests.IsExistLocally([]string{tests.Out + fileutils.GetFileSeparator() + "a2.in"}, paths, t)

	// Download the third file, expecting to download a3.in
	artifactoryCli.Exec("download", tests.Repo1+"/offset_test/", tests.Out+"/", "--flat=true", "--sort-by=name", "--limit=1", "--offset=2")
	paths, _ = fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	tests.IsExistLocally([]string{tests.Out + fileutils.GetFileSeparator() + "a3.in"}, paths, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryCopyByBuildOverridingByInlineFlag(t *testing.T) {
	initArtifactoryTest(t)
	buildName, buildNumberA, buildNumberB := "cli-test-build", "10", "11"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.CopyByBuildSpec)
	if err != nil {
		t.Error(err)
	}

	// Upload files with buildName and buildNumber: b* uploaded with build number "10", a* uploaded with build number "11"
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--build-name="+buildName, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--build-name="+buildName, "--build-number="+buildNumberB)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildName, buildNumberA)
	artifactoryCli.Exec("build-publish", buildName, buildNumberB)

	// Copy by build number: using override of build by flag from inline (no number set so LATEST build should be copied), a* should be copied
	artifactoryCli.Exec("copy", "--build="+buildName+" --spec="+specFile)

	// Validate files are Copied by build number
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetBuildCopyExpected(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

func TestArtifactoryMoveByBuildUsingFlags(t *testing.T) {
	initArtifactoryTest(t)
	buildName, buildNumberA, buildNumberB := "cli-test-build", "10", "11"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.CopyByBuildSpec)
	if err != nil {
		t.Error(err)
	}

	// Upload files with buildName and buildNumber
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--build-name="+buildName, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--build-name="+buildName, "--build-number="+buildNumberB)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildName, buildNumberA)
	artifactoryCli.Exec("build-publish", buildName, buildNumberB)

	// Move by build name and number
	artifactoryCli.Exec("move", "--build="+buildName+"/11 --spec="+specFile)

	// Validate files are moved by build number
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetBuildMoveExpected(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

func TestArtifactoryMoveNoSpec(t *testing.T) {
	testCopyMoveNoSpec("mv", tests.GetBuildBeforeMoveExpected(), tests.GetBuildMoveExpected(), t)
}

func TestArtifactoryMoveExcludeByCli(t *testing.T) {
	initArtifactoryTest(t)

	// Upload files
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA)
	artifactoryCli.Exec("upload", "--spec="+specFileB)

	// Move by pattern
	artifactoryCli.Exec("move", tests.Repo1+"/data/ "+tests.Repo2+"/", "--exclude-patterns=*b*;*c*")

	// Validate excluded files didn't move
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetBuildMoveExpected(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryMoveExcludeBySpec(t *testing.T) {
	initArtifactoryTest(t)
	specFile, err := tests.CreateSpec(tests.MoveCopySpecExclude)
	if err != nil {
		t.Error(err)
	}

	// Upload files
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileA)
	artifactoryCli.Exec("upload", "--spec="+specFileB)

	// Move by spec
	artifactoryCli.Exec("move", "--spec="+specFile)

	// Validate excluded files didn't move
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetBuildMoveExpected(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	cleanArtifactoryTest()
}

func TestArtifactoryDeleteByLatestBuild(t *testing.T) {
	initArtifactoryTest(t)
	buildName, buildNumberA, buildNumberB := "cli-test-build", "10", "11"
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	specFile, err := tests.CreateSpec(tests.CopyByBuildSpec)
	if err != nil {
		t.Error(err)
	}

	// Upload files with buildName and buildNumber
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFileB, "--build-name="+buildName, "--build-number="+buildNumberA)
	artifactoryCli.Exec("upload", "--spec="+specFileA, "--build-name="+buildName, "--build-number="+buildNumberB)

	// Publish buildInfo
	artifactoryCli.Exec("build-publish", buildName, buildNumberA)
	artifactoryCli.Exec("build-publish", buildName, buildNumberB)

	// Delete by build name and LATEST
	artifactoryCli.Exec("delete", "--build="+buildName+"/LATEST --quiet=true --spec="+specFile)

	// Validate files are deleted by build number
	cpMvDlByBuildAssertSpec, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}

	isExistInArtifactory(tests.GetBuildDeleteExpected(), cpMvDlByBuildAssertSpec, t)

	// Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

func TestGitLfsCleanup(t *testing.T) {
	initArtifactoryTest(t)
	var filePath = "testsdata/gitlfs/(4b)(*)"
	artifactoryCli.Exec("upload", filePath, tests.LfsRepo+"/objects/4b/f4/{2}{1}")
	artifactoryCli.Exec("upload", filePath, tests.LfsRepo+"/objects/4b/f4/")
	refs := strings.Join([]string{"refs", "heads", "*"}, fileutils.GetFileSeparator())
	dotGitPath := getCliDotGitPath(t)
	artifactoryCli.Exec("glc", dotGitPath, "--repo="+tests.LfsRepo, "--refs=HEAD,"+refs, "--quiet=true")
	gitlfsSpecFile, err := tests.CreateSpec(tests.GitLfsAssertSpec)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(tests.GetGitLfsExpected(), gitlfsSpecFile, t)
	cleanArtifactoryTest()
}

func TestPing(t *testing.T) {
	initArtifactoryTest(t)
	artifactoryCli.Exec("ping")
	cleanArtifactoryTest()
}

func TestSummaryReport(t *testing.T) {
	initArtifactoryTest(t)

	previousLog := log.Logger
	newLog := log.NewLogger(logUtils.GetCliLogLevel(), nil)

	// Set new logger with output redirection to buffer
	buffer := &bytes.Buffer{}
	newLog.SetOutputWriter(buffer)
	log.SetLogger(newLog)

	specFile, err := tests.CreateSpec(tests.SimpleUploadSpec)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+specFile)
	verifySummary(t, buffer, 9, 0, previousLog)

	artifactoryCli.Exec("move", path.Join(tests.Repo1, "*.in"), tests.Repo2+"/")
	verifySummary(t, buffer, 9, 0, previousLog)

	artifactoryCli.Exec("copy", path.Join(tests.Repo2, "*.in"), tests.Repo1+"/")
	verifySummary(t, buffer, 9, 0, previousLog)

	artifactoryCli.Exec("delete", path.Join(tests.Repo2, "*.in"), "--quiet=true")
	verifySummary(t, buffer, 9, 0, previousLog)

	artifactoryCli.Exec("set-props", path.Join(tests.Repo1, "*.in"), "prop=val")
	verifySummary(t, buffer, 9, 0, previousLog)

	specFile, err = tests.CreateSpec(tests.DownloadSpec)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("download", "--spec="+specFile)
	verifySummary(t, buffer, 10, 0, previousLog)

	// Restore previous logger
	log.SetLogger(previousLog)
	cleanArtifactoryTest()
}

func TestArtifactoryBuildDiscard(t *testing.T) {
	// Initialize
	initArtifactoryTest(t)
	client, err := httpclient.ClientBuilder().Build()
	if err != nil {
		t.Error(err)
	}

	// Upload files with buildName and buildNumber
	buildName := "discard-builds-test"
	for i := 1; i <= 10; i++ {
		artifactoryCli.Exec("upload", "testsdata/a/(*)", tests.Repo1+"/data/{1}", "--build-name="+buildName, "--build-number="+strconv.Itoa(i))
		artifactoryCli.Exec("build-publish", buildName, strconv.Itoa(i))
	}

	// Test discard by max-builds
	artifactoryCli.Exec("build-discard", buildName, "--max-builds=8")
	jsonResponse := getAllBuildsByBuildName(client, buildName, t, http.StatusOK)
	if len(jsonResponse.Builds) != 8 {
		t.Error("Incorrect operation of build-discard by max-builds.")
	}

	// Test discard with exclusion
	artifactoryCli.Exec("build-discard", buildName, "--max-days=-1", "--exclude-builds=2,3,4,5,6,7,8,9,10")
	jsonResponse = getAllBuildsByBuildName(client, buildName, t, http.StatusOK)
	if len(jsonResponse.Builds) != 8 {
		t.Error("Incorrect operation of build-discard with exclusion.")
	}

	// Test discard by max-days
	artifactoryCli.Exec("build-discard", buildName, "--max-days=-1")
	jsonResponse = getAllBuildsByBuildName(client, buildName, t, http.StatusNotFound)
	if len(jsonResponse.Builds) != 0 {
		t.Error("Incorrect operation of build-discard by max-days.")
	}

	//Cleanup
	inttestutils.DeleteBuild(artifactoryDetails.Url, buildName, artHttpDetails)
	cleanArtifactoryTest()
}

// Tests compatibility to file paths with windows separators.
// Verifies the upload and download commands work as expected for inputs of both arguments and spec files.
func TestArtifactoryWinBackwardsCompatibility(t *testing.T) {
	if !cliutils.IsWindows() {
		return
	}
	initArtifactoryTest(t)
	uploadSpecFile, err := tests.CreateSpec(tests.WinSimpleUploadSpec)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("upload", "--spec="+uploadSpecFile)
	artifactoryCli.Exec("upload", "testsdata\\\\a\\\\b\\\\*", tests.Repo1+"/compatibility_arguments/", "--exclude-patterns=*b2.in;*c*")

	downloadSpecFile, err := tests.CreateSpec(tests.WinSimpleDownloadSpec)
	if err != nil {
		t.Error(err)
	}
	artifactoryCli.Exec("download", "--spec="+downloadSpecFile)
	artifactoryCli.Exec("download", tests.Repo1+"/*arguments*", "out\\\\win\\\\", "--flat=true")

	// Validate files are downloaded by build number
	paths, _ := fileutils.ListFilesRecursiveWalkIntoDirSymlink(tests.Out, false)
	err = tests.ValidateListsIdentical(tests.GetWinCompatibility(), paths)
	if err != nil {
		t.Error(err.Error())
	}
	cleanArtifactoryTest()
}

func getAllBuildsByBuildName(client *httpclient.HttpClient, buildName string, t *testing.T, expectedHttpStatusCode int) buildsApiResponseStruct {
	resp, body, _, _ := client.SendGet(artifactoryDetails.Url+"api/build/"+buildName, true, artHttpDetails)
	if resp.StatusCode != expectedHttpStatusCode {
		t.Error("Failed retrieving build information from artifactory.")
	}

	buildsApiResponse := &buildsApiResponseStruct{}
	err := json.Unmarshal(body, buildsApiResponse)
	if err != nil {
		t.Error("Unmarshaling failed with an error:", err.Error())
	}
	return *buildsApiResponse
}

type buildsApiInnerBuildsStruct struct {
	Uri     string `json:"uri,omitempty"`
	Started string `json:"started,omitempty"`
}

type buildsApiResponseStruct struct {
	Uri    string                       `json:"uri,omitempty"`
	Builds []buildsApiInnerBuildsStruct `json:"buildsNumbers,omitempty"`
}

func verifySummary(t *testing.T, buffer *bytes.Buffer, success, failure int64, logger log.Log) {
	content := buffer.Bytes()
	buffer.Reset()
	logger.Output(string(content))

	status, err := jsonparser.GetString(content, "status")
	if err != nil {
		log.Error(err)
		t.Error(err)
	}
	if status != "success" {
		t.Error("Summary validation failed, expected: success, got:", status)
	}

	resultSuccess, err := jsonparser.GetInt(content, "totals", "success")
	if err != nil {
		log.Error(err)
		t.Error(err)
	}

	resultFailure, err := jsonparser.GetInt(content, "totals", "failure")
	if err != nil {
		log.Error(err)
		t.Error(err)
	}

	if resultSuccess != success {
		t.Error("Summary validation failed, expected success count:", success, "got:", resultSuccess)
	}
	if resultFailure != failure {
		t.Error("Summary validation failed, expected failure count:", failure, "got:", resultFailure)
	}
}

func CleanArtifactoryTests() {
	cleanArtifactoryTest()
	deleteRepos()
}

func initArtifactoryTest(t *testing.T) {
	if !*tests.TestArtifactory {
		t.Skip("Artifactory is not being tested, skipping...")
	}
}

func cleanArtifactoryTest() {
	if !*tests.TestArtifactory {
		return
	}
	os.Unsetenv(cliutils.JfrogHomeDirEnv)
	log.Info("Cleaning test data...")
	cleanArtifactory()
	tests.CleanFileSystem()
}

func prepUploadFiles() {
	uploadPath := tests.GetTestResourcesPath() + "(.*)"
	targetPath := tests.Repo1 + "/downloadTestResources/{1}"
	flags := "--threads=10 --regexp=true --props=searchMe=true --flat=false"
	artifactoryCli.Exec("upload", uploadPath, targetPath, flags)
}

func prepCopyFiles() error {
	specFile, err := tests.CreateSpec(tests.PrepareCopy)
	if err != nil {
		return err
	}
	artifactoryCli.Exec("copy", "--spec="+specFile)
	return nil
}

func getPathsToDelete(specFile string) []rtutils.ResultItem {
	deleteSpec, _ := spec.CreateSpecFromFile(specFile, nil)
	deleteCommand := generic.NewDeleteCommand()
	deleteCommand.SetRtDetails(artifactoryDetails).SetSpec(deleteSpec).SetDryRun(false)
	deleteCommand.GetPathsToDelete()
	return deleteCommand.DeleteItems()
}

func execDeleteRepoRest(repoName string) {
	client, err := httpclient.ClientBuilder().Build()
	if err != nil {
		log.Error(err)
		os.Exit(1)
		return
	}
	resp, body, err := client.SendDelete(artifactoryDetails.Url+"api/repositories/"+repoName, nil, artHttpDetails)
	if err != nil {
		log.Error(err)
		return
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		log.Error(errors.New("Artifactory response: " + resp.Status + "\n" + clientutils.IndentJson(body)))
		return
	}
	log.Info("Repository", repoName, "deleted.")
}

func execCreateRepoRest(repoConfig, repoName string) {
	content, err := ioutil.ReadFile(repoConfig)
	if err != nil {
		log.Error(err)
		os.Exit(1)
		return
	}
	rtutils.AddHeader("Content-Type", "application/json", &artHttpDetails.Headers)
	client, err := httpclient.ClientBuilder().Build()
	if err != nil {
		log.Error(err)
		os.Exit(1)
		return
	}
	resp, body, err := client.SendPut(artifactoryDetails.Url+"api/repositories/"+repoName, content, artHttpDetails)
	if err != nil {
		log.Error(err)
		os.Exit(1)
		return
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		log.Error(errors.New("Artifactory response: " + resp.Status + "\n" + clientutils.IndentJson(body)))
		os.Exit(1)
		return
	}
	log.Info("Repository", repoName, "created.")
}

func createReposIfNeeded() {
	createRandomReposName()
	nonVirtualRepos := tests.GetNonVirtualRepositories()
	createRepos(nonVirtualRepos)
	virtualRepos := tests.GetVirtualRepositories()
	createRepos(virtualRepos)
}

func createRepos(repos map[string]string) {
	for repoName, configFile := range repos {
		if !isRepoExist(repoName) {
			repoConfig := tests.GetTestResourcesPath() + configFile
			repoConfig, err := tests.ReplaceTemplateVariables(repoConfig, "")
			if err != nil {
				log.Error(err)
				os.Exit(1)
			}
			execCreateRepoRest(repoConfig, repoName)
		}
	}
}

func createRandomReposName() {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	tests.Repo1 += "-" + timestamp
	tests.Repo2 += "-" + timestamp
	tests.VirtualRepo += "-" + timestamp
	tests.LfsRepo += "-" + timestamp
	if *tests.TestBuildTools {
		tests.JcenterRemoteRepo += "-" + timestamp
		tests.NpmLocalRepo += "-" + timestamp
		tests.NpmRemoteRepo += "-" + timestamp
	}
	if *tests.TestGo {
		tests.GoLocalRepo += "-" + timestamp
	}
}

func deleteRepos() {
	repos := []string{
		tests.VirtualRepo,
		tests.Repo1,
		tests.Repo2,
		tests.LfsRepo,
	}

	if *tests.TestBuildTools {
		repos = append(repos, tests.JcenterRemoteRepo, tests.NpmLocalRepo, tests.NpmRemoteRepo)
	}

	for _, repoName := range repos {
		if isRepoExist(repoName) {
			execDeleteRepoRest(repoName)
		}
	}
}

func cleanArtifactory() {
	deleteSpecFile := tests.GetFilePathForArtifactory(tests.DeleteSpec)
	fmt.Println(deleteSpecFile)
	deleteSpecFile, err := tests.ReplaceTemplateVariables(deleteSpecFile, "")
	fmt.Println(deleteSpecFile)
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}
	deleteSpec, _ := spec.CreateSpecFromFile(deleteSpecFile, nil)
	tests.DeleteFiles(deleteSpec, artifactoryDetails)
}

func searchInArtifactory(specFile string) ([]generic.SearchResult, error) {
	searchSpec, _ := spec.CreateSpecFromFile(specFile, nil)
	searchCmd := generic.NewSearchCommand()
	searchCmd.SetRtDetails(artifactoryDetails).SetSpec(searchSpec)
	err := searchCmd.Search()
	return searchCmd.SearchResult(), err
}

func getSpecAndCommonFlags(specFile string) (*spec.SpecFiles, rtutils.CommonConf) {
	searchFlags := new(rtutils.CommonConfImpl)
	searchSpec, _ := spec.CreateSpecFromFile(specFile, nil)
	return searchSpec, searchFlags
}

func isExistInArtifactory(expected []string, specFile string, t *testing.T) {
	results, _ := searchInArtifactory(specFile)
	tests.CompareExpectedVsActuals(expected, results, t)
}

func isExistInArtifactoryByProps(expected []string, pattern, props string, t *testing.T) {
	searchSpec := spec.NewBuilder().Pattern(pattern).Props(props).Recursive(true).BuildSpec()
	searchCmd := generic.NewSearchCommand()
	searchCmd.SetRtDetails(artifactoryDetails).SetSpec(searchSpec)
	err := searchCmd.Search()
	if err != nil {
		t.Error(err)
	}
	tests.CompareExpectedVsActuals(expected, searchCmd.SearchResult(), t)
}

func isRepoExist(repoName string) bool {
	client, err := httpclient.ClientBuilder().Build()
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}
	resp, _, _, err := client.SendGet(artifactoryDetails.Url+tests.RepoDetailsUrl+repoName, true, artHttpDetails)
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}

	if resp.StatusCode != http.StatusBadRequest {
		return true
	}
	return false
}

func getCliDotGitPath(t *testing.T) string {
	dotGitPath, err := os.Getwd()
	if err != nil {
		t.Error("Failed to get current dir.")
	}
	dotGitExists, err := fileutils.IsDirExists(filepath.Join(dotGitPath, ".git"), false)
	if err != nil {
		t.Error(err)
	}
	if !dotGitExists {
		t.Error("Can't find .git")
	}
	return dotGitPath
}

func deleteServerConfig() {
	configArtifactoryCli.Exec("c", "delete", tests.RtServerId, "--interactive=false")
}

// This function will create server config and return the entire passphrase flag if it needed.
// For example if passphrase is needed it will return "--ssh-passphrase=${theConfiguredPassphrase}" or empty string.
func createServerConfigAndReturnPassphrase() (passphrase string) {
	if *tests.RtSshPassphrase != "" {
		passphrase = "--ssh-passphrase=" + *tests.RtSshPassphrase
	}
	configArtifactoryCli.Exec("c", tests.RtServerId, "--interactive=false")
	return passphrase
}

func testCopyMoveNoSpec(command string, beforeCommandExpected, afterCommandExpected []string, t *testing.T) {
	initArtifactoryTest(t)

	// Upload files
	specFileA, err := tests.CreateSpec(tests.SplitUploadSpecA)
	if err != nil {
		t.Error(err)
	}
	specFileB, err := tests.CreateSpec(tests.SplitUploadSpecB)
	if err != nil {
		t.Error(err)
	}

	artifactoryCli.Exec("upload", "--spec="+specFileA)
	artifactoryCli.Exec("upload", "--spec="+specFileB)

	// Run command with dry-run
	artifactoryCli.Exec(command, tests.Repo1+"/data/*a* "+tests.Repo2+"/", "--dry-run")

	// Validate files weren't affected
	cpMvSpecFilePath, err := tests.CreateSpec(tests.CpMvDlByBuildAssertSpec)
	if err != nil {
		t.Error(err)
	}
	isExistInArtifactory(beforeCommandExpected, cpMvSpecFilePath, t)

	// Run command
	artifactoryCli.Exec(command, tests.Repo1+"/data/*a* "+tests.Repo2+"/")

	// Validate files were affected
	isExistInArtifactory(afterCommandExpected, cpMvSpecFilePath, t)

	// Cleanup
	cleanArtifactoryTest()
}

func searchItemsInArtifacotry(t *testing.T) []rtutils.ResultItem {
	fileSpec, err := tests.CreateSpec(tests.Search)
	if err != nil {
		t.Error(err)
	}
	spec, flags := getSpecAndCommonFlags(fileSpec)
	flags.SetArtifactoryDetails(artAuth)
	var resultItems []rtutils.ResultItem
	for i := 0; i < len(spec.Files); i++ {

		searchParams, err := generic.GetSearchParams(spec.Get(i))
		if err != nil {
			t.Error(err)
			t.FailNow()
		}

		currentResultItems, err := services.SearchBySpecFiles(searchParams, flags, rtutils.ALL)
		if err != nil {
			t.Error("Failed Searching files:", err)
			t.FailNow()
		}
		resultItems = append(resultItems, currentResultItems...)
	}
	return resultItems
}
