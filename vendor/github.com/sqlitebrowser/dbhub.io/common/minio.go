package common

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/minio/minio-go"
)

var (
	// Minio connection handle
	minioClient *minio.Client
)

// ConnectMinio parses the Minio configuration, to ensure it seems workable
// Note - this doesn't actually open a connection to the Minio server.
func ConnectMinio() (err error) {
	// Connect to the Minio server
	minioClient, err = minio.New(Conf.Minio.Server, Conf.Minio.AccessKey, Conf.Minio.Secret, Conf.Minio.HTTPS)
	if err != nil {
		return fmt.Errorf("Problem with Minio server configuration: %v\n", err)
	}

	// Log Minio server end point
	log.Printf("Minio server config ok. Address: %v\n", Conf.Minio.Server)
	return nil
}

// MinioHandle gets a handle from Minio for a SQLite database object
func MinioHandle(bucket, id string) (*minio.Object, error) {
	userDB, err := minioClient.GetObject(bucket, id, minio.GetObjectOptions{})
	if err != nil {
		log.Printf("Error retrieving DB from Minio: %v\n", err)
		return nil, errors.New("Error retrieving database from internal storage")
	}
	return userDB, nil
}

// MinioHandleClose closes a Minio object handle.  Probably most useful for calling with defer()
func MinioHandleClose(userDB *minio.Object) (err error) {
	err = userDB.Close()
	if err != nil {
		log.Printf("Error closing object handle: %v\n", err)
	}
	return
}

// RetrieveDatabaseFile retrieves a SQLite database file from Minio.  If there's a locally cached version already
// available though, use that
func RetrieveDatabaseFile(bucket, id string) (newDB string, err error) {
	// Check if the database file already exists
	newDB = filepath.Join(Conf.DiskCache.Directory, bucket, id)
	if _, err = os.Stat(newDB); os.IsNotExist(err) {
		// * The database doesn't yet exist locally, so fetch it from Minio

		// Check if a the database file is already being fetched from Minio by a different caller
		//  eg check if there is a "<filename>.new" file already in the disk cache
		if _, err = os.Stat(newDB + ".new"); os.IsNotExist(err) {
			// * The database isn't already being fetched, so we're ok to proceed

			// Get a handle from Minio for the database object
			var userDB *minio.Object
			userDB, err = MinioHandle(bucket, id)
			if err != nil {
				return "", err
			}

			// Close the object handle when this function finishes
			defer func() {
				MinioHandleClose(userDB)
			}()

			// Create the needed directory path in the disk cache
			err = os.MkdirAll(filepath.Join(Conf.DiskCache.Directory, bucket), 0750)

			// Save the database locally to the local disk cache, with ".new" on the end (will be renamed after file is
			// finished writing)
			var f *os.File
			f, err = os.OpenFile(newDB+".new", os.O_CREATE|os.O_WRONLY, 0750)
			if err != nil {
				log.Printf("Error creating new database file in the disk cache: %v\n", err)
				return "", errors.New("Internal server error")
			}
			bytesWritten, err := io.Copy(f, userDB)
			if err != nil {
				log.Printf("Error writing to new database file in the disk cache : %v\n", err)
				return "", errors.New("Internal server error")
			}
			if bytesWritten == 0 {
				log.Printf("0 bytes written to the new SQLite database file: %s\n", newDB+".new")
				return "", errors.New("Internal server error")
			}
			f.Close()

			// Now that the database file has been fully written to disk, remove the .new on the end of the name
			err = os.Rename(newDB+".new", newDB)
			if err != nil {
				log.Printf("Error when renaming .new database file to final form in the disk cache: %s\n", err.Error())
				return "", errors.New("Internal server error")
			}
		} else {
			// TODO: This is not a great approach, but should be ok for initial "get it working" code.
			// TODO  Instead, it should probably loop around a few times checking for the file to be finished being
			// TODO  created.
			// TODO  Also, it's probably a decent idea to compare the file timestamp details os.Chtimes()? with the
			// TODO  current system time, to detect and handle the case where the "<filename>.new" file is a stale one
			// TODO  left over from some other (interrupted) process.  In which case nuke that and proceed to recreate
			// TODO  it.
			return "", errors.New("Database retrieval in progress, try again in a few seconds")
		}
	}
	return
}

// StoreDatabaseFile stores a database file in Minio
func StoreDatabaseFile(db *os.File, sha string, dbSize int64) error {
	bkt := sha[:MinioFolderChars]
	id := sha[MinioFolderChars:]

	// If a Minio bucket with the desired name doesn't already exist, create it
	found, err := minioClient.BucketExists(bkt)
	if err != nil {
		log.Printf("Error when checking if Minio bucket '%s' already exists: %v\n", bkt, err)
		return err
	}
	if !found {
		err := minioClient.MakeBucket(bkt, "us-east-1")
		if err != nil {
			log.Printf("Error creating Minio bucket '%v': %v\n", bkt, err)
			return err
		}
	}

	// Store the SQLite database file in Minio
	numBytes, err := minioClient.PutObject(bkt, id, db, dbSize, minio.PutObjectOptions{ContentType: "application/x-sqlite3"})
	if err != nil {
		log.Printf("Storing file in Minio failed: %v\n", err)
		return err
	}

	// Sanity check.  Make sure the # of bytes written is equal to the size of the buffer we were given
	if dbSize != numBytes {
		log.Printf("Something went wrong storing the database file.  dbSize = %v, numBytes = %v\n", dbSize,
			numBytes)
		return err
	}
	return nil
}
