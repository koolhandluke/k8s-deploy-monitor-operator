package dispatch

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// metav1Options returns a default UpdateOptions for dynamic client calls.
func metav1Options() metav1.UpdateOptions {
	return metav1.UpdateOptions{}
}

// metav1GetOptions returns a default GetOptions for dynamic client calls.
func metav1GetOptions() metav1.GetOptions {
	return metav1.GetOptions{}
}

// metav1ListOptions returns a default ListOptions for dynamic client calls.
func metav1ListOptions() metav1.ListOptions {
	return metav1.ListOptions{}
}

// metav1DeleteOptions returns a default DeleteOptions for dynamic client calls.
func metav1DeleteOptions() metav1.DeleteOptions {
	return metav1.DeleteOptions{}
}
