package test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/authzed/spicedb/internal/datastore"
	"github.com/authzed/spicedb/internal/testfixtures"
	pb "github.com/authzed/spicedb/pkg/REDACTEDapi/api"
	"github.com/authzed/spicedb/pkg/tuple"
)

const (
	testUserNamespace     = "test/user"
	testResourceNamespace = "test/resource"
	testReaderRelation    = "reader"
	ellipsis              = "..."
)

func TestSimple(t *testing.T, tester DatastoreTester) {
	testCases := []int{1, 2, 4, 32, 1024}

	for _, numTuples := range testCases {
		t.Run(strconv.Itoa(numTuples), func(t *testing.T) {
			require := require.New(t)

			ds, err := tester.New(0)
			require.NoError(err)

			setupDatastore(ds, require)

			tRequire := testfixtures.TupleChecker{Require: require, DS: ds}

			var testTuples []*pb.RelationTuple

			var lastRevision uint64
			for i := 0; i < numTuples; i++ {
				resourceName := fmt.Sprintf("resource%d", i)
				userName := fmt.Sprintf("user%d", i)

				newTuple := makeTestTuple(resourceName, userName)
				testTuples = append(testTuples, newTuple)

				writtenAt, err := ds.WriteTuples(
					nil,
					[]*pb.RelationTupleUpdate{tuple.Create(newTuple)},
				)
				require.NoError(err)
				require.Greater(writtenAt, lastRevision)

				tRequire.TupleExists(newTuple, writtenAt)
				tRequire.TupleExists(newTuple, writtenAt+100)
				tRequire.NoTupleExists(newTuple, writtenAt-1)

				lastRevision = writtenAt
			}

			for _, tupleToFind := range testTuples {
				// Check that we can find the tuple a number of ways
				q := ds.QueryTuples(tupleToFind.ObjectAndRelation.Namespace, lastRevision)

				queries := []datastore.TupleQuery{
					q.WithObjectID(tupleToFind.ObjectAndRelation.ObjectId),
					q.WithUserset(tupleToFind.User.GetUserset()),
					q.WithObjectID(tupleToFind.ObjectAndRelation.ObjectId).WithRelation(tupleToFind.ObjectAndRelation.Relation),
					q.WithObjectID(tupleToFind.ObjectAndRelation.ObjectId).WithUserset(tupleToFind.User.GetUserset()),
					q.WithRelation(tupleToFind.ObjectAndRelation.Relation).WithUserset(tupleToFind.User.GetUserset()),
				}
				for _, query := range queries {
					iter, err := query.Execute()
					require.NoError(err)
					tRequire.VerifyIteratorResults(iter, tupleToFind)
				}
			}

			// Check that we can find the group of tuples too
			q := ds.QueryTuples(testTuples[0].ObjectAndRelation.Namespace, lastRevision)

			queries := []datastore.TupleQuery{
				q,
				q.WithRelation(testTuples[0].ObjectAndRelation.Relation),
			}
			for _, query := range queries {
				iter, err := query.Execute()
				require.NoError(err)
				tRequire.VerifyIteratorResults(iter, testTuples...)
			}

			// Try some bad queries
			badQueries := []datastore.TupleQuery{
				q.WithObjectID("fakeobjectid"),
				q.WithUserset(&pb.ObjectAndRelation{
					Namespace: "test/user",
					ObjectId:  "fakeuser",
					Relation:  ellipsis,
				}),
			}
			for _, badQuery := range badQueries {
				iter, err := badQuery.Execute()
				require.NoError(err)
				tRequire.VerifyIteratorResults(iter)
			}

			// Delete the first tuple
			deletedAt, err := ds.WriteTuples(
				nil,
				[]*pb.RelationTupleUpdate{tuple.Delete(testTuples[0])},
			)
			require.NoError(err)

			// Verify it can still be read at the old revision
			tRequire.TupleExists(testTuples[0], deletedAt-1)

			// Verify that it does not show up at the new revision
			tRequire.NoTupleExists(testTuples[0], deletedAt)
			alreadyDeletedIter, err := ds.QueryTuples(
				testTuples[0].ObjectAndRelation.Namespace,
				deletedAt,
			).Execute()
			require.NoError(err)
			tRequire.VerifyIteratorResults(alreadyDeletedIter, testTuples[1:]...)
		})
	}
}

func TestPreconditions(t *testing.T, tester DatastoreTester) {
	require := require.New(t)

	ds, err := tester.New(0)
	require.NoError(err)

	setupDatastore(ds, require)

	first := makeTestTuple("first", "owner")
	second := makeTestTuple("second", "owner")

	_, err = ds.WriteTuples(
		[]*pb.RelationTuple{first},
		[]*pb.RelationTupleUpdate{tuple.Create(second)},
	)
	require.True(errors.Is(err, datastore.ErrPreconditionFailed))

	_, err = ds.WriteTuples(nil, []*pb.RelationTupleUpdate{tuple.Create(first)})
	require.NoError(err)

	_, err = ds.WriteTuples(
		[]*pb.RelationTuple{first},
		[]*pb.RelationTupleUpdate{tuple.Create(second)},
	)
	require.NoError(err)
}

func TestWriteInvalidTuples(t *testing.T, tester DatastoreTester) {
	testCases := []struct {
		tupleToWrite  string
		expectedError error
	}{
		{"test/resource:res1#reader@test/resource:res2#...", nil},
		{"fakenamespace:nil#nil@nil:nil#nil", datastore.ErrNamespaceNotFound},
		{"test/resource:res#fakerelation@nil:nil#nil", datastore.ErrRelationNotFound},
		{"test/resource:res1#reader@fakenamespace:nil#...", datastore.ErrNamespaceNotFound},
		{"test/resource:res1#reader@test/resource:res2#fakerelation", datastore.ErrRelationNotFound},
	}

	for _, tc := range testCases {
		name := fmt.Sprintf("%s=>%s", tc.tupleToWrite, tc.expectedError)
		t.Run(name, func(t *testing.T) {
			require := require.New(t)

			ds, err := tester.New(0)
			require.NoError(err)

			setupDatastore(ds, require)

			tpl := tuple.Scan(tc.tupleToWrite)
			require.NotNil(tpl)

			_, err = ds.WriteTuples(nil, []*pb.RelationTupleUpdate{tuple.Create(tpl)})
			require.Equal(tc.expectedError, err)

			_, err = ds.WriteTuples(nil, []*pb.RelationTupleUpdate{tuple.Touch(tpl)})
			require.Equal(tc.expectedError, err)

			_, err = ds.WriteTuples(nil, []*pb.RelationTupleUpdate{tuple.Delete(tpl)})
			require.Equal(tc.expectedError, err)
		})
	}
}

func TestRevisionFuzzing(t *testing.T, tester DatastoreTester) {
	require := require.New(t)

	fuzzingRange := 50 * time.Millisecond

	ds, err := tester.New(fuzzingRange)
	require.NoError(err)

	setupDatastore(ds, require)

	// Create some revisions
	tpl := makeTestTuple("first", "owner")
	for i := 0; i < 10; i++ {
		_, err = ds.WriteTuples(nil, []*pb.RelationTupleUpdate{tuple.Touch(tpl)})
		require.NoError(err)
	}

	// Get the new now revision
	nowRevision, err := ds.SyncRevision(context.Background())
	require.NoError(err)
	require.GreaterOrEqual(nowRevision, uint64(0))

	foundAnotherRevision := false
	for start := time.Now(); time.Since(start) < 10*time.Millisecond; {
		testRevision, err := ds.Revision(context.Background())
		require.NoError(err)
		require.LessOrEqual(testRevision, nowRevision)
		if testRevision < nowRevision {
			foundAnotherRevision = true
			break
		}
	}

	require.True(foundAnotherRevision)

	// Let the fuzzing window expire
	time.Sleep(fuzzingRange)

	// Now we should ONLY get the now revision
	for start := time.Now(); time.Since(start) < 10*time.Millisecond; {
		testRevision, err := ds.Revision(context.Background())
		require.NoError(err)
		require.Equal(nowRevision, testRevision)
	}
}
