package dispatch

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func metav1Options() metav1.UpdateOptions {
	return metav1.UpdateOptions{}
}

func metav1GetOptions() metav1.GetOptions {
	return metav1.GetOptions{}
}

func metav1ListOptions() metav1.ListOptions {
	return metav1.ListOptions{}
}

func metav1DeleteOptions() metav1.DeleteOptions {
	return metav1.DeleteOptions{}
}
