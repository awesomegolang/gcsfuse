// Copyright 2015 Google Inc. All Rights Reserved.
// Author: jacobsa@google.com (Aaron Jacobs)

package gcsproxy_test

import (
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/jacobsa/gcloud/gcs/mock_gcs"
	"github.com/jacobsa/gcsfuse/gcsproxy"
	"github.com/jacobsa/gcsfuse/timeutil"
	. "github.com/jacobsa/oglematchers"
	"github.com/jacobsa/oglemock"
	. "github.com/jacobsa/ogletest"
	"golang.org/x/net/context"
	"google.golang.org/cloud/storage"
)

func TestListingProxy(t *testing.T) { RunTests(t) }

////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////

type ObjectSlice []*storage.Object

func (s ObjectSlice) Len() int           { return len(s) }
func (s ObjectSlice) Less(i, j int) bool { return s[i].Name < s[j].Name }
func (s ObjectSlice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func sortObjectsByName(s []*storage.Object) []*storage.Object {
	sortable := ObjectSlice(s)
	sort.Sort(sortable)
	return sortable
}

func sortStrings(s []string) []string {
	sortable := sort.StringSlice(s)
	sort.Sort(sortable)
	return sortable
}

////////////////////////////////////////////////////////////////////////
// Invariant-checking listing proxy
////////////////////////////////////////////////////////////////////////

// A wrapper around ListingProxy that calls CheckInvariants whenever invariants
// should hold. For catching logic errors early in the test.
type checkingListingProxy struct {
	wrapped *gcsproxy.ListingProxy
}

func (lp *checkingListingProxy) Name() string {
	lp.wrapped.CheckInvariants()
	defer lp.wrapped.CheckInvariants()
	return lp.wrapped.Name()
}

func (lp *checkingListingProxy) List() ([]*storage.Object, []string, error) {
	lp.wrapped.CheckInvariants()
	defer lp.wrapped.CheckInvariants()
	return lp.wrapped.List(context.Background())
}

func (lp *checkingListingProxy) NoteNewObject(o *storage.Object) error {
	lp.wrapped.CheckInvariants()
	defer lp.wrapped.CheckInvariants()
	return lp.wrapped.NoteNewObject(o)
}

func (lp *checkingListingProxy) NoteNewSubdirectory(name string) error {
	lp.wrapped.CheckInvariants()
	defer lp.wrapped.CheckInvariants()
	return lp.wrapped.NoteNewSubdirectory(name)
}

func (lp *checkingListingProxy) NoteRemoval(name string) error {
	lp.wrapped.CheckInvariants()
	defer lp.wrapped.CheckInvariants()
	return lp.wrapped.NoteRemoval(name)
}

////////////////////////////////////////////////////////////////////////
// Boilerplate
////////////////////////////////////////////////////////////////////////

type ListingProxyTest struct {
	dirName string
	bucket  mock_gcs.MockBucket
	clock   timeutil.SimulatedClock
	lp      checkingListingProxy
}

var _ SetUpInterface = &ListingProxyTest{}

func init() { RegisterTestSuite(&ListingProxyTest{}) }

func (t *ListingProxyTest) SetUp(ti *TestInfo) {
	t.dirName = "some/dir/"
	t.bucket = mock_gcs.NewMockBucket(ti.MockController, "bucket")

	var err error
	t.lp.wrapped, err = gcsproxy.NewListingProxy(t.bucket, &t.clock, t.dirName)
	if err != nil {
		panic(err)
	}
}

////////////////////////////////////////////////////////////////////////
// Test functions
////////////////////////////////////////////////////////////////////////

func (t *ListingProxyTest) CreateForRootDirectory() {
	_, err := gcsproxy.NewListingProxy(t.bucket, &t.clock, "")
	AssertEq(nil, err)
}

func (t *ListingProxyTest) CreateForIllegalDirectoryName() {
	_, err := gcsproxy.NewListingProxy(t.bucket, &t.clock, "foo/bar")

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("foo/bar")))
	ExpectThat(err, Error(HasSubstr("directory name")))
}

func (t *ListingProxyTest) Name() {
	ExpectEq(t.dirName, t.lp.Name())
}

func (t *ListingProxyTest) List_CallsBucket() {
	// Bucket.ListObjects
	var query *storage.Query
	saveQuery := func(
		ctx context.Context,
		q *storage.Query) (*storage.Objects, error) {
		query = q
		return nil, errors.New("")
	}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Invoke(saveQuery))

	// List
	t.lp.List()

	AssertNe(nil, query)
	ExpectEq("/", query.Delimiter)
	ExpectEq(t.dirName, query.Prefix)
	ExpectFalse(query.Versions)
	ExpectEq("", query.Cursor)
	ExpectEq(0, query.MaxResults)
}

func (t *ListingProxyTest) List_BucketFails() {
	// Bucket.ListObjects
	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(nil, errors.New("taco")))

	// List
	_, _, err := t.lp.List()

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("List")))
	ExpectThat(err, Error(HasSubstr("taco")))
}

func (t *ListingProxyTest) List_BucketReturnsIllegalObjectName() {
	badObj := &storage.Object{
		Name: t.dirName + "foo/",
	}

	badListing := &storage.Objects{
		Results: []*storage.Object{badObj},
	}

	// Bucket.ListObjects
	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(badListing, nil))

	// List
	_, _, err := t.lp.List()

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("object name")))
	ExpectThat(err, Error(HasSubstr(badObj.Name)))
}

func (t *ListingProxyTest) List_BucketReturnsIllegalDirectoryName() {
	badListing := &storage.Objects{
		Prefixes: []string{
			t.dirName + "foo/",
			t.dirName + "bar",
			t.dirName + "baz/",
		},
	}

	// Bucket.ListObjects
	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(badListing, nil))

	// List
	_, _, err := t.lp.List()

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("directory name")))
	ExpectThat(err, Error(HasSubstr(badListing.Prefixes[1])))
}

func (t *ListingProxyTest) List_BucketReturnsNonDescendantObject() {
	badObj := &storage.Object{
		Name: "some/other/dir/obj",
	}

	badListing := &storage.Objects{
		Results: []*storage.Object{badObj},
	}

	// Bucket.ListObjects
	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(badListing, nil))

	// List
	_, _, err := t.lp.List()

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("object")))
	ExpectThat(err, Error(HasSubstr(badObj.Name)))
	ExpectThat(err, Error(HasSubstr("descendant")))
}

func (t *ListingProxyTest) List_BucketReturnsNonDescendantPrefix() {
	badListing := &storage.Objects{
		Prefixes: []string{
			"some/other/dir/",
		},
	}

	// Bucket.ListObjects
	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(badListing, nil))

	// List
	_, _, err := t.lp.List()

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("some/other/dir/")))
	ExpectThat(err, Error(HasSubstr("descendant")))
}

func (t *ListingProxyTest) List_EmptyResult() {
	// Bucket.ListObjects
	listing := &storage.Objects{}
	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	// List
	objects, subdirs, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(objects, ElementsAre())
	ExpectThat(subdirs, ElementsAre())
}

func (t *ListingProxyTest) List_OnlyPlaceholderForProxiedDir() {
	// Bucket.ListObjects
	listing := &storage.Objects{
		Results: []*storage.Object{
			&storage.Object{Name: t.dirName},
		},
	}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	// List
	objects, subdirs, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(objects, ElementsAre())
	ExpectThat(subdirs, ElementsAre())
}

func (t *ListingProxyTest) List_NonEmptyResult_PlaceholderForProxiedDirPresent() {
	// Bucket.ListObjects
	listing := &storage.Objects{
		Results: []*storage.Object{
			&storage.Object{Name: t.dirName},
			&storage.Object{Name: t.dirName + "bar"},
			&storage.Object{Name: t.dirName + "foo"},
		},
		Prefixes: []string{
			t.dirName + "baz/",
			t.dirName + "qux/",
		},
	}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	// List
	objects, subdirs, err := t.lp.List()

	objects = sortObjectsByName(objects)
	subdirs = sortStrings(subdirs)

	AssertEq(nil, err)
	ExpectThat(objects, ElementsAre(listing.Results[1], listing.Results[2]))
	ExpectThat(subdirs, ElementsAre(listing.Prefixes[0], listing.Prefixes[1]))
}

func (t *ListingProxyTest) List_NonEmptyResult_PlaceholderForProxiedDirNotPresent() {
	// Bucket.ListObjects
	listing := &storage.Objects{
		Results: []*storage.Object{
			&storage.Object{Name: t.dirName + "bar"},
			&storage.Object{Name: t.dirName + "foo"},
		},
		Prefixes: []string{
			t.dirName + "baz/",
			t.dirName + "qux/",
		},
	}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	// List
	objects, subdirs, err := t.lp.List()

	objects = sortObjectsByName(objects)
	subdirs = sortStrings(subdirs)

	AssertEq(nil, err)
	ExpectThat(objects, ElementsAre(listing.Results[0], listing.Results[1]))
	ExpectThat(subdirs, ElementsAre(listing.Prefixes[0], listing.Prefixes[1]))
}

func (t *ListingProxyTest) List_CacheIsValid() {
	// List once.
	listing := &storage.Objects{
		Results: []*storage.Object{
			&storage.Object{Name: t.dirName + "foo"},
		},
		Prefixes: []string{
			t.dirName + "baz/",
		},
	}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, _, err := t.lp.List()
	AssertEq(nil, err)

	// Move into the future, but not quite too far.
	t.clock.AdvanceTime(gcsproxy.ListingProxy_ListingCacheTTL - time.Millisecond)

	// List again. Without any work, the results should be correct.
	objects, subdirs, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(objects, ElementsAre(listing.Results[0]))
	ExpectThat(subdirs, ElementsAre(listing.Prefixes[0]))
}

func (t *ListingProxyTest) List_CacheHasExpired() {
	// List successfully.
	listing := &storage.Objects{}
	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, _, err := t.lp.List()
	AssertEq(nil, err)

	// Move just slightly too far into the future.
	t.clock.AdvanceTime(gcsproxy.ListingProxy_ListingCacheTTL + time.Millisecond)

	// We should need to fall through to the bucket.
	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, errors.New("")))

	t.lp.List()
}

func (t *ListingProxyTest) NoteNewObject_IllegalNames() {
	var err error
	try := func(name string) error {
		return t.lp.NoteNewObject(&storage.Object{Name: name})
	}

	// Equal to directory name
	err = try(t.dirName)

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("Illegal object name")))
	ExpectThat(err, Error(HasSubstr(t.dirName)))

	// Sub-directory name
	err = try(t.dirName + "subdir/")

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("Illegal object name")))
	ExpectThat(err, Error(HasSubstr("subdir/")))

	// Non-descendant
	err = try("some/other/dir/obj")

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("descendant")))
	ExpectThat(err, Error(HasSubstr("some/other/dir/obj")))
}

func (t *ListingProxyTest) NoteNewObject_NewListingRequired_NoConflict() {
	var err error
	o := &storage.Object{
		Name: t.dirName + "foo",
	}

	// Note an object.
	err = t.lp.NoteNewObject(o)
	AssertEq(nil, err)

	// Simulate a successful listing from GCS not containing that name.
	listing := &storage.Objects{}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	objects, _, err := t.lp.List()
	AssertEq(nil, err)

	// The sole entry should be the new object.
	ExpectThat(objects, ElementsAre(o))
}

func (t *ListingProxyTest) NoteNewObject_NewListingRequired_Conflict() {
	var err error
	o := &storage.Object{
		Name: t.dirName + "foo",
	}

	// Note an object.
	err = t.lp.NoteNewObject(o)
	AssertEq(nil, err)

	// Simulate a successful listing from GCS containing a different entry for
	// that name.
	listing := &storage.Objects{
		Results: []*storage.Object{
			&storage.Object{
				Name: o.Name,
			},
		},
	}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	objects, _, err := t.lp.List()
	AssertEq(nil, err)

	// The sole entry should be the new object.
	ExpectThat(objects, ElementsAre(o))
}

func (t *ListingProxyTest) NoteNewObject_PrevListingConflicts() {
	var err error
	name := t.dirName + "foo"

	// Simulate a successful listing from GCS containing an entry for the object
	// of interest.
	listing := &storage.Objects{
		Results: []*storage.Object{
			&storage.Object{
				Name: name,
			},
		},
	}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, _, err = t.lp.List()
	AssertEq(nil, err)

	// Note a different version of the object.
	o := &storage.Object{
		Name: name,
	}

	err = t.lp.NoteNewObject(o)
	AssertEq(nil, err)

	// List again. We should get the new version.
	objects, _, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(objects, ElementsAre(o))
}

func (t *ListingProxyTest) NoteNewObject_PrevListingDoesntConflict() {
	var err error
	name := t.dirName + "foo"

	// Simulate a successful listing from GCS containing nothing of interest.
	listing := &storage.Objects{}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, _, err = t.lp.List()
	AssertEq(nil, err)

	// Note an object.
	o := &storage.Object{
		Name: name,
	}

	err = t.lp.NoteNewObject(o)
	AssertEq(nil, err)

	// List again. We should get the new object.
	objects, _, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(objects, ElementsAre(o))
}

func (t *ListingProxyTest) NoteNewObject_PreviousAddition() {
	var err error
	name := t.dirName + "foo"

	// Simulate a successful listing from GCS containing nothing of interest.
	listing := &storage.Objects{}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, _, err = t.lp.List()
	AssertEq(nil, err)

	// Note an object.
	err = t.lp.NoteNewObject(&storage.Object{Name: name})
	AssertEq(nil, err)

	// Note it again.
	o := &storage.Object{
		Name: name,
	}

	err = t.lp.NoteNewObject(o)
	AssertEq(nil, err)

	// List again. We should get the new version.
	objects, _, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(objects, ElementsAre(o))
}

func (t *ListingProxyTest) NoteNewObject_PreviousRemoval() {
	var err error
	name := t.dirName + "foo"

	// Simulate a successful listing from GCS containing nothing of interest.
	listing := &storage.Objects{}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, _, err = t.lp.List()
	AssertEq(nil, err)

	// Mark an object as removed.
	err = t.lp.NoteRemoval(name)
	AssertEq(nil, err)

	// Note the object as added.
	o := &storage.Object{
		Name: name,
	}

	err = t.lp.NoteNewObject(o)
	AssertEq(nil, err)

	// List again. We should get the noted new object.
	objects, _, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(objects, ElementsAre(o))
}

func (t *ListingProxyTest) NoteNewSubdirectory_IllegalNames() {
	var err error
	try := func(name string) error {
		return t.lp.NoteNewSubdirectory(name)
	}

	// Object name
	err = try(t.dirName + "foo")

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("Illegal sub-directory name")))
	ExpectThat(err, Error(HasSubstr("foo")))

	// Non-descendant
	err = try("some/other/dir/")

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("descendant")))
	ExpectThat(err, Error(HasSubstr("some/other/dir/")))

	// Equal to directory name
	err = try(t.dirName)

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("descendant")))
	ExpectThat(err, Error(HasSubstr(t.dirName)))

	// Descendant but not immediate
	err = try(t.dirName + "subdir/other/")

	AssertNe(nil, err)
	ExpectThat(err, Error(HasSubstr("direct descendant")))
	ExpectThat(err, Error(HasSubstr("subdir/other/")))
}

func (t *ListingProxyTest) NoteNewSubdirectory_NewListingRequired_NoConflict() {
	var err error
	name := t.dirName + "foo/"

	// Note a sub-directory.
	err = t.lp.NoteNewSubdirectory(name)
	AssertEq(nil, err)

	// Simulate a successful listing from GCS not containing that name.
	listing := &storage.Objects{}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, subdirs, err := t.lp.List()
	AssertEq(nil, err)

	// The sole entry should be the new sub-dir.
	ExpectThat(subdirs, ElementsAre(name))
}

func (t *ListingProxyTest) NoteNewSubdirectory_NewListingRequired_Conflict() {
	var err error
	name := t.dirName + "foo/"

	// Note a sub-directory.
	err = t.lp.NoteNewSubdirectory(name)
	AssertEq(nil, err)

	// Simulate a successful listing from GCS containing that name.
	listing := &storage.Objects{
		Prefixes: []string{name},
	}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, subdirs, err := t.lp.List()
	AssertEq(nil, err)

	// The sole entry should be the name.
	ExpectThat(subdirs, ElementsAre(name))
}

func (t *ListingProxyTest) NoteNewSubdirectory_PrevListingConflicts() {
	var err error
	name := t.dirName + "foo/"

	// Simulate a successful listing from GCS containing an entry for the name of
	// interest.
	listing := &storage.Objects{
		Prefixes: []string{name},
	}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, _, err = t.lp.List()
	AssertEq(nil, err)

	// Note the addition of the sub-directory.
	err = t.lp.NoteNewSubdirectory(name)
	AssertEq(nil, err)

	// List again. We should get only one record.
	_, subdirs, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(subdirs, ElementsAre(name))
}

func (t *ListingProxyTest) NoteNewSubdirectory_PrevListingDoesntConflict() {
	var err error
	name := t.dirName + "foo/"

	// Simulate a successful listing from GCS containing nothing of interest.
	listing := &storage.Objects{}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, _, err = t.lp.List()
	AssertEq(nil, err)

	// Note the addition of the sub-directory.
	err = t.lp.NoteNewSubdirectory(name)
	AssertEq(nil, err)

	// List again. We should get the sub-dir.
	_, subdirs, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(subdirs, ElementsAre(name))
}

func (t *ListingProxyTest) NoteNewSubdirectory_PreviousAddition() {
	var err error
	name := t.dirName + "foo/"

	// Simulate a successful listing from GCS containing nothing of interest.
	listing := &storage.Objects{}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, _, err = t.lp.List()
	AssertEq(nil, err)

	// Note the addition of the sub-directory.
	err = t.lp.NoteNewSubdirectory(name)
	AssertEq(nil, err)

	// And again.
	err = t.lp.NoteNewSubdirectory(name)
	AssertEq(nil, err)

	// List again. We should get only one record.
	_, subdirs, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(subdirs, ElementsAre(name))
}

func (t *ListingProxyTest) NoteNewSubdirectory_PreviousRemoval() {
	var err error
	name := t.dirName + "foo/"

	// Simulate a successful listing from GCS containing nothing of interest.
	listing := &storage.Objects{}

	ExpectCall(t.bucket, "ListObjects")(Any(), Any()).
		WillOnce(oglemock.Return(listing, nil))

	_, _, err = t.lp.List()
	AssertEq(nil, err)

	// Note the removal of the sub-directory.
	err = t.lp.NoteRemoval(name)
	AssertEq(nil, err)

	// Note the addition.
	err = t.lp.NoteNewSubdirectory(name)
	AssertEq(nil, err)

	// List again. We should see it.
	_, subdirs, err := t.lp.List()

	AssertEq(nil, err)
	ExpectThat(subdirs, ElementsAre(name))
}

func (t *ListingProxyTest) NoteRemoval_NoPreviousListing() {
	AssertTrue(false, "TODO")
}

func (t *ListingProxyTest) NoteRemoval_PrevListingConflicts() {
	AssertTrue(false, "TODO")
}

func (t *ListingProxyTest) NoteRemoval_PrevListingDoesntConflict() {
	AssertTrue(false, "TODO")
}

func (t *ListingProxyTest) NoteRemoval_PreviousAddition() {
	AssertTrue(false, "TODO")
}

func (t *ListingProxyTest) NoteRemoval_PreviousRemoval() {
	AssertTrue(false, "TODO")
}
